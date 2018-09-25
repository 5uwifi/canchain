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

var (
	nodeDBNilID          = ID{}
	nodeDBNodeExpiration = 24 * time.Hour
	nodeDBCleanupCycle   = time.Hour
	nodeDBVersion        = 6
)

type DB struct {
	lvl    *leveldb.DB
	runner sync.Once
	quit   chan struct{}
}

var (
	nodeDBVersionKey = []byte("version")
	nodeDBItemPrefix = []byte("n:")

	nodeDBDiscoverRoot      = ":discover"
	nodeDBDiscoverPing      = nodeDBDiscoverRoot + ":lastping"
	nodeDBDiscoverPong      = nodeDBDiscoverRoot + ":lastpong"
	nodeDBDiscoverFindFails = nodeDBDiscoverRoot + ":findfail"
)

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
	currentVer = currentVer[:binary.PutVarint(currentVer, int64(nodeDBVersion))]

	blob, err := db.Get(nodeDBVersionKey, nil)
	switch err {
	case leveldb.ErrNotFound:
		if err := db.Put(nodeDBVersionKey, currentVer, nil); err != nil {
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
	if bytes.Equal(id[:], nodeDBNilID[:]) {
		return []byte(field)
	}
	return append(nodeDBItemPrefix, append(id[:], field...)...)
}

func splitKey(key []byte) (id ID, field string) {
	if !bytes.HasPrefix(key, nodeDBItemPrefix) {
		return ID{}, string(key)
	}
	item := key[len(nodeDBItemPrefix):]
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

func (db *DB) Node(id ID) *Node {
	blob, err := db.lvl.Get(makeKey(id, nodeDBDiscoverRoot), nil)
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
	blob, err := rlp.EncodeToBytes(&node.r)
	if err != nil {
		return err
	}
	return db.lvl.Put(makeKey(node.ID(), nodeDBDiscoverRoot), blob, nil)
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
	tick := time.NewTicker(nodeDBCleanupCycle)
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
	threshold := time.Now().Add(-nodeDBNodeExpiration)

	it := db.lvl.NewIterator(nil, nil)
	defer it.Release()

	for it.Next() {
		id, field := splitKey(it.Key())
		if field != nodeDBDiscoverRoot {
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
	return time.Unix(db.fetchInt64(makeKey(id, nodeDBDiscoverPing)), 0)
}

func (db *DB) UpdateLastPingReceived(id ID, instance time.Time) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverPing), instance.Unix())
}

func (db *DB) LastPongReceived(id ID) time.Time {
	db.ensureExpirer()
	return time.Unix(db.fetchInt64(makeKey(id, nodeDBDiscoverPong)), 0)
}

func (db *DB) UpdateLastPongReceived(id ID, instance time.Time) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverPong), instance.Unix())
}

func (db *DB) FindFails(id ID) int {
	return int(db.fetchInt64(makeKey(id, nodeDBDiscoverFindFails)))
}

func (db *DB) UpdateFindFails(id ID, fails int) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverFindFails), int64(fails))
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
		it.Seek(makeKey(id, nodeDBDiscoverRoot))

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
		if field != nodeDBDiscoverRoot {
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
