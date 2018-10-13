package cnode

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	dbVersionKey = "version"
	dbItemPrefix = "n:"

	dbDiscoverRoot      = ":discover"
	dbDiscoverSeq       = dbDiscoverRoot + ":seq"
	dbDiscoverPing      = dbDiscoverRoot + ":lastping"
	dbDiscoverPong      = dbDiscoverRoot + ":lastpong"
	dbDiscoverFindFails = dbDiscoverRoot + ":findfail"
	dbLocalRoot         = ":local"
	dbLocalSeq          = dbLocalRoot + ":seq"
)

var (
	dbNodeExpiration = 24 * time.Hour
	dbCleanupCycle   = time.Hour
	dbVersion        = 7
)

type DB struct {
	lvl    *leveldb.DB
	runner sync.Once
	quit   chan struct{}
}

func OpenDB(path string) (*DB, error) {
	if path == "" {
		return newMemoryDB()
	}
	return newPersistentDB(path)
}

func newMemoryDB() (*DB, error) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		return nil, err
	}
	return &DB{lvl: db, quit: make(chan struct{})}, nil
}

func newPersistentDB(path string) (*DB, error) {
	opts := &opt.Options{OpenFilesCacheCapacity: 5}
	db, err := leveldb.OpenFile(path, opts)
	if _, iscorrupted := err.(*errors.ErrCorrupted); iscorrupted {
		db, err = leveldb.RecoverFile(path, nil)
	}
	if err != nil {
		return nil, err
	}
	currentVer := make([]byte, binary.MaxVarintLen64)
	currentVer = currentVer[:binary.PutVarint(currentVer, int64(dbVersion))]

	blob, err := db.Get([]byte(dbVersionKey), nil)
	switch err {
	case leveldb.ErrNotFound:
		if err := db.Put([]byte(dbVersionKey), currentVer, nil); err != nil {
			db.Close()
			return nil, err
		}

	case nil:
		if !bytes.Equal(blob, currentVer) {
			db.Close()
			if err = os.RemoveAll(path); err != nil {
				return nil, err
			}
			return newPersistentDB(path)
		}
	}
	return &DB{lvl: db, quit: make(chan struct{})}, nil
}

func makeKey(id ID, field string) []byte {
	if (id == ID{}) {
		return []byte(field)
	}
	return append([]byte(dbItemPrefix), append(id[:], field...)...)
}

func splitKey(key []byte) (id ID, field string) {
	if !bytes.HasPrefix(key, []byte(dbItemPrefix)) {
		return ID{}, string(key)
	}
	item := key[len(dbItemPrefix):]
	copy(id[:], item[:len(id)])
	field = string(item[len(id):])

	return id, field
}

func (db *DB) fetchInt64(key []byte) int64 {
	blob, err := db.lvl.Get(key, nil)
	if err != nil {
		return 0
	}
	val, read := binary.Varint(blob)
	if read <= 0 {
		return 0
	}
	return val
}

func (db *DB) storeInt64(key []byte, n int64) error {
	blob := make([]byte, binary.MaxVarintLen64)
	blob = blob[:binary.PutVarint(blob, n)]
	return db.lvl.Put(key, blob, nil)
}

func (db *DB) fetchUint64(key []byte) uint64 {
	blob, err := db.lvl.Get(key, nil)
	if err != nil {
		return 0
	}
	val, _ := binary.Uvarint(blob)
	return val
}

func (db *DB) storeUint64(key []byte, n uint64) error {
	blob := make([]byte, binary.MaxVarintLen64)
	blob = blob[:binary.PutUvarint(blob, n)]
	return db.lvl.Put(key, blob, nil)
}

func (db *DB) Node(id ID) *Node {
	blob, err := db.lvl.Get(makeKey(id, dbDiscoverRoot), nil)
	if err != nil {
		return nil
	}
	return mustDecodeNode(id[:], blob)
}

func mustDecodeNode(id, data []byte) *Node {
	node := new(Node)
	if err := rlp.DecodeBytes(data, &node.r); err != nil {
		panic(fmt.Errorf("p2p/ccnode: can't decode node %x in DB: %v", id, err))
	}
	copy(node.id[:], id)
	return node
}

func (db *DB) UpdateNode(node *Node) error {
	if node.Seq() < db.NodeSeq(node.ID()) {
		return nil
	}
	blob, err := rlp.EncodeToBytes(&node.r)
	if err != nil {
		return err
	}
	if err := db.lvl.Put(makeKey(node.ID(), dbDiscoverRoot), blob, nil); err != nil {
		return err
	}
	return db.storeUint64(makeKey(node.ID(), dbDiscoverSeq), node.Seq())
}

func (db *DB) NodeSeq(id ID) uint64 {
	return db.fetchUint64(makeKey(id, dbDiscoverSeq))
}

func (db *DB) Resolve(n *Node) *Node {
	if n.Seq() > db.NodeSeq(n.ID()) {
		return n
	}
	return db.Node(n.ID())
}

func (db *DB) DeleteNode(id ID) error {
	deleter := db.lvl.NewIterator(util.BytesPrefix(makeKey(id, "")), nil)
	for deleter.Next() {
		if err := db.lvl.Delete(deleter.Key(), nil); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) ensureExpirer() {
	db.runner.Do(func() { go db.expirer() })
}

func (db *DB) expirer() {
	tick := time.NewTicker(dbCleanupCycle)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if err := db.expireNodes(); err != nil {
				log4j.Error("Failed to expire nodedb items", "err", err)
			}
		case <-db.quit:
			return
		}
	}
}

func (db *DB) expireNodes() error {
	threshold := time.Now().Add(-dbNodeExpiration)

	it := db.lvl.NewIterator(nil, nil)
	defer it.Release()

	for it.Next() {
		id, field := splitKey(it.Key())
		if field != dbDiscoverRoot {
			continue
		}
		if seen := db.LastPongReceived(id); seen.After(threshold) {
			continue
		}
		db.DeleteNode(id)
	}
	return nil
}

func (db *DB) LastPingReceived(id ID) time.Time {
	return time.Unix(db.fetchInt64(makeKey(id, dbDiscoverPing)), 0)
}

func (db *DB) UpdateLastPingReceived(id ID, instance time.Time) error {
	return db.storeInt64(makeKey(id, dbDiscoverPing), instance.Unix())
}

func (db *DB) LastPongReceived(id ID) time.Time {
	db.ensureExpirer()
	return time.Unix(db.fetchInt64(makeKey(id, dbDiscoverPong)), 0)
}

func (db *DB) UpdateLastPongReceived(id ID, instance time.Time) error {
	return db.storeInt64(makeKey(id, dbDiscoverPong), instance.Unix())
}

func (db *DB) FindFails(id ID) int {
	return int(db.fetchInt64(makeKey(id, dbDiscoverFindFails)))
}

func (db *DB) UpdateFindFails(id ID, fails int) error {
	return db.storeInt64(makeKey(id, dbDiscoverFindFails), int64(fails))
}

func (db *DB) localSeq(id ID) uint64 {
	return db.fetchUint64(makeKey(id, dbLocalSeq))
}

func (db *DB) storeLocalSeq(id ID, n uint64) {
	db.storeUint64(makeKey(id, dbLocalSeq), n)
}

func (db *DB) QuerySeeds(n int, maxAge time.Duration) []*Node {
	var (
		now   = time.Now()
		nodes = make([]*Node, 0, n)
		it    = db.lvl.NewIterator(nil, nil)
		id    ID
	)
	defer it.Release()

seek:
	for seeks := 0; len(nodes) < n && seeks < n*5; seeks++ {
		ctr := id[0]
		rand.Read(id[:])
		id[0] = ctr + id[0]%16
		it.Seek(makeKey(id, dbDiscoverRoot))

		n := nextNode(it)
		if n == nil {
			id[0] = 0
			continue seek
		}
		if now.Sub(db.LastPongReceived(n.ID())) > maxAge {
			continue seek
		}
		for i := range nodes {
			if nodes[i].ID() == n.ID() {
				continue seek
			}
		}
		nodes = append(nodes, n)
	}
	return nodes
}

func nextNode(it iterator.Iterator) *Node {
	for end := false; !end; end = !it.Next() {
		id, field := splitKey(it.Key())
		if field != dbDiscoverRoot {
			continue
		}
		return mustDecodeNode(id[:], it.Value())
	}
	return nil
}

func (db *DB) Close() {
	close(db.quit)
	db.lvl.Close()
}
