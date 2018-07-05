//
// (at your option) any later version.
//
//

package trie

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/metrics"
	"github.com/5uwifi/canchain/basis/rlp"
)

var (
	memcacheFlushTimeTimer  = metrics.NewRegisteredResettingTimer("trie/memcache/flush/time", nil)
	memcacheFlushNodesMeter = metrics.NewRegisteredMeter("trie/memcache/flush/nodes", nil)
	memcacheFlushSizeMeter  = metrics.NewRegisteredMeter("trie/memcache/flush/size", nil)

	memcacheGCTimeTimer  = metrics.NewRegisteredResettingTimer("trie/memcache/gc/time", nil)
	memcacheGCNodesMeter = metrics.NewRegisteredMeter("trie/memcache/gc/nodes", nil)
	memcacheGCSizeMeter  = metrics.NewRegisteredMeter("trie/memcache/gc/size", nil)

	memcacheCommitTimeTimer  = metrics.NewRegisteredResettingTimer("trie/memcache/commit/time", nil)
	memcacheCommitNodesMeter = metrics.NewRegisteredMeter("trie/memcache/commit/nodes", nil)
	memcacheCommitSizeMeter  = metrics.NewRegisteredMeter("trie/memcache/commit/size", nil)
)

var secureKeyPrefix = []byte("secure-key-")

const secureKeyLength = 11 + 32

type DatabaseReader interface {
	// Get retrieves the value associated with key form the database.
	Get(key []byte) (value []byte, err error)

	// Has retrieves whether a key is present in the database.
	Has(key []byte) (bool, error)
}

type Database struct {
	diskdb candb.Database // Persistent storage for matured trie nodes

	nodes  map[common.Hash]*cachedNode // Data and references relationships of a node
	oldest common.Hash                 // Oldest tracked node, flush-list head
	newest common.Hash                 // Newest tracked node, flush-list tail

	preimages map[common.Hash][]byte // Preimages of nodes from the secure trie
	seckeybuf [secureKeyLength]byte  // Ephemeral buffer for calculating preimage keys

	gctime  time.Duration      // Time spent on garbage collection since last commit
	gcnodes uint64             // Nodes garbage collected since last commit
	gcsize  common.StorageSize // Data storage garbage collected since last commit

	flushtime  time.Duration      // Time spent on data flushing since last commit
	flushnodes uint64             // Nodes flushed since last commit
	flushsize  common.StorageSize // Data storage flushed since last commit

	nodesSize     common.StorageSize // Storage size of the nodes cache (exc. flushlist)
	preimagesSize common.StorageSize // Storage size of the preimages cache

	lock sync.RWMutex
}

type rawNode []byte

func (n rawNode) canUnload(uint16, uint16) bool { panic("this should never end up in a live trie") }
func (n rawNode) cache() (hashNode, bool)       { panic("this should never end up in a live trie") }
func (n rawNode) fstring(ind string) string     { panic("this should never end up in a live trie") }

type rawFullNode [17]node

func (n rawFullNode) canUnload(uint16, uint16) bool { panic("this should never end up in a live trie") }
func (n rawFullNode) cache() (hashNode, bool)       { panic("this should never end up in a live trie") }
func (n rawFullNode) fstring(ind string) string     { panic("this should never end up in a live trie") }

func (n rawFullNode) EncodeRLP(w io.Writer) error {
	var nodes [17]node

	for i, child := range n {
		if child != nil {
			nodes[i] = child
		} else {
			nodes[i] = nilValueNode
		}
	}
	return rlp.Encode(w, nodes)
}

type rawShortNode struct {
	Key []byte
	Val node
}

func (n rawShortNode) canUnload(uint16, uint16) bool { panic("this should never end up in a live trie") }
func (n rawShortNode) cache() (hashNode, bool)       { panic("this should never end up in a live trie") }
func (n rawShortNode) fstring(ind string) string     { panic("this should never end up in a live trie") }

type cachedNode struct {
	node node   // Cached collapsed trie node, or raw rlp data
	size uint16 // Byte size of the useful cached data

	parents  uint16                 // Number of live nodes referencing this one
	children map[common.Hash]uint16 // External children referenced by this node

	flushPrev common.Hash // Previous node in the flush-list
	flushNext common.Hash // Next node in the flush-list
}

func (n *cachedNode) rlp() []byte {
	if node, ok := n.node.(rawNode); ok {
		return node
	}
	blob, err := rlp.EncodeToBytes(n.node)
	if err != nil {
		panic(err)
	}
	return blob
}

func (n *cachedNode) obj(hash common.Hash, cachegen uint16) node {
	if node, ok := n.node.(rawNode); ok {
		return mustDecodeNode(hash[:], node, cachegen)
	}
	return expandNode(hash[:], n.node, cachegen)
}

func (n *cachedNode) childs() []common.Hash {
	children := make([]common.Hash, 0, 16)
	for child := range n.children {
		children = append(children, child)
	}
	if _, ok := n.node.(rawNode); !ok {
		gatherChildren(n.node, &children)
	}
	return children
}

func gatherChildren(n node, children *[]common.Hash) {
	switch n := n.(type) {
	case *rawShortNode:
		gatherChildren(n.Val, children)

	case rawFullNode:
		for i := 0; i < 16; i++ {
			gatherChildren(n[i], children)
		}
	case hashNode:
		*children = append(*children, common.BytesToHash(n))

	case valueNode, nil:

	default:
		panic(fmt.Sprintf("unknown node type: %T", n))
	}
}

func simplifyNode(n node) node {
	switch n := n.(type) {
	case *shortNode:
		// Short nodes discard the flags and cascade
		return &rawShortNode{Key: n.Key, Val: simplifyNode(n.Val)}

	case *fullNode:
		// Full nodes discard the flags and cascade
		node := rawFullNode(n.Children)
		for i := 0; i < len(node); i++ {
			if node[i] != nil {
				node[i] = simplifyNode(node[i])
			}
		}
		return node

	case valueNode, hashNode, rawNode:
		return n

	default:
		panic(fmt.Sprintf("unknown node type: %T", n))
	}
}

func expandNode(hash hashNode, n node, cachegen uint16) node {
	switch n := n.(type) {
	case *rawShortNode:
		// Short nodes need key and child expansion
		return &shortNode{
			Key: compactToHex(n.Key),
			Val: expandNode(nil, n.Val, cachegen),
			flags: nodeFlag{
				hash: hash,
				gen:  cachegen,
			},
		}

	case rawFullNode:
		// Full nodes need child expansion
		node := &fullNode{
			flags: nodeFlag{
				hash: hash,
				gen:  cachegen,
			},
		}
		for i := 0; i < len(node.Children); i++ {
			if n[i] != nil {
				node.Children[i] = expandNode(nil, n[i], cachegen)
			}
		}
		return node

	case valueNode, hashNode:
		return n

	default:
		panic(fmt.Sprintf("unknown node type: %T", n))
	}
}

func NewDatabase(diskdb candb.Database) *Database {
	return &Database{
		diskdb:    diskdb,
		nodes:     map[common.Hash]*cachedNode{{}: {}},
		preimages: make(map[common.Hash][]byte),
	}
}

func (db *Database) DiskDB() DatabaseReader {
	return db.diskdb
}

func (db *Database) InsertBlob(hash common.Hash, blob []byte) {
	db.lock.Lock()
	defer db.lock.Unlock()

	db.insert(hash, blob, rawNode(blob))
}

func (db *Database) insert(hash common.Hash, blob []byte, node node) {
	// If the node's already cached, skip
	if _, ok := db.nodes[hash]; ok {
		return
	}
	// Create the cached entry for this node
	entry := &cachedNode{
		node:      simplifyNode(node),
		size:      uint16(len(blob)),
		flushPrev: db.newest,
	}
	for _, child := range entry.childs() {
		if c := db.nodes[child]; c != nil {
			c.parents++
		}
	}
	db.nodes[hash] = entry

	// Update the flush-list endpoints
	if db.oldest == (common.Hash{}) {
		db.oldest, db.newest = hash, hash
	} else {
		db.nodes[db.newest].flushNext, db.newest = hash, hash
	}
	db.nodesSize += common.StorageSize(common.HashLength + entry.size)
}

//
func (db *Database) insertPreimage(hash common.Hash, preimage []byte) {
	if _, ok := db.preimages[hash]; ok {
		return
	}
	db.preimages[hash] = common.CopyBytes(preimage)
	db.preimagesSize += common.StorageSize(common.HashLength + len(preimage))
}

func (db *Database) node(hash common.Hash, cachegen uint16) node {
	// Retrieve the node from cache if available
	db.lock.RLock()
	node := db.nodes[hash]
	db.lock.RUnlock()

	if node != nil {
		return node.obj(hash, cachegen)
	}
	// Content unavailable in memory, attempt to retrieve from disk
	enc, err := db.diskdb.Get(hash[:])
	if err != nil || enc == nil {
		return nil
	}
	return mustDecodeNode(hash[:], enc, cachegen)
}

func (db *Database) Node(hash common.Hash) ([]byte, error) {
	// Retrieve the node from cache if available
	db.lock.RLock()
	node := db.nodes[hash]
	db.lock.RUnlock()

	if node != nil {
		return node.rlp(), nil
	}
	// Content unavailable in memory, attempt to retrieve from disk
	return db.diskdb.Get(hash[:])
}

func (db *Database) preimage(hash common.Hash) ([]byte, error) {
	// Retrieve the node from cache if available
	db.lock.RLock()
	preimage := db.preimages[hash]
	db.lock.RUnlock()

	if preimage != nil {
		return preimage, nil
	}
	// Content unavailable in memory, attempt to retrieve from disk
	return db.diskdb.Get(db.secureKey(hash[:]))
}

func (db *Database) secureKey(key []byte) []byte {
	buf := append(db.seckeybuf[:0], secureKeyPrefix...)
	buf = append(buf, key...)
	return buf
}

func (db *Database) Nodes() []common.Hash {
	db.lock.RLock()
	defer db.lock.RUnlock()

	var hashes = make([]common.Hash, 0, len(db.nodes))
	for hash := range db.nodes {
		if hash != (common.Hash{}) { // Special case for "root" references/nodes
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

func (db *Database) Reference(child common.Hash, parent common.Hash) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	db.reference(child, parent)
}

func (db *Database) reference(child common.Hash, parent common.Hash) {
	// If the node does not exist, it's a node pulled from disk, skip
	node, ok := db.nodes[child]
	if !ok {
		return
	}
	// If the reference already exists, only duplicate for roots
	if db.nodes[parent].children == nil {
		db.nodes[parent].children = make(map[common.Hash]uint16)
	} else if _, ok = db.nodes[parent].children[child]; ok && parent != (common.Hash{}) {
		return
	}
	node.parents++
	db.nodes[parent].children[child]++
}

func (db *Database) Dereference(root common.Hash) {
	db.lock.Lock()
	defer db.lock.Unlock()

	nodes, storage, start := len(db.nodes), db.nodesSize, time.Now()
	db.dereference(root, common.Hash{})

	db.gcnodes += uint64(nodes - len(db.nodes))
	db.gcsize += storage - db.nodesSize
	db.gctime += time.Since(start)

	memcacheGCTimeTimer.Update(time.Since(start))
	memcacheGCSizeMeter.Mark(int64(storage - db.nodesSize))
	memcacheGCNodesMeter.Mark(int64(nodes - len(db.nodes)))

	log4j.Debug("Dereferenced trie from memory database", "nodes", nodes-len(db.nodes), "size", storage-db.nodesSize, "time", time.Since(start),
		"gcnodes", db.gcnodes, "gcsize", db.gcsize, "gctime", db.gctime, "livenodes", len(db.nodes), "livesize", db.nodesSize)
}

func (db *Database) dereference(child common.Hash, parent common.Hash) {
	// Dereference the parent-child
	node := db.nodes[parent]

	if node.children != nil && node.children[child] > 0 {
		node.children[child]--
		if node.children[child] == 0 {
			delete(node.children, child)
		}
	}
	// If the child does not exist, it's a previously committed node.
	node, ok := db.nodes[child]
	if !ok {
		return
	}
	// If there are no more references to the child, delete it and cascade
	node.parents--
	if node.parents == 0 {
		// Remove the node from the flush-list
		if child == db.oldest {
			db.oldest = node.flushNext
		} else {
			db.nodes[node.flushPrev].flushNext = node.flushNext
			db.nodes[node.flushNext].flushPrev = node.flushPrev
		}
		// Dereference all children and delete the node
		for _, hash := range node.childs() {
			db.dereference(hash, child)
		}
		delete(db.nodes, child)
		db.nodesSize -= common.StorageSize(common.HashLength + int(node.size))
	}
}

func (db *Database) Cap(limit common.StorageSize) error {
	// Create a database batch to flush persistent data out. It is important that
	// outside code doesn't see an inconsistent state (referenced data removed from
	// memory cache during commit but not yet in persistent storage). This is ensured
	// by only uncaching existing data when the database write finalizes.
	db.lock.RLock()

	nodes, storage, start := len(db.nodes), db.nodesSize, time.Now()
	batch := db.diskdb.NewBatch()

	// db.nodesSize only contains the useful data in the cache, but when reporting
	// the total memory consumption, the maintenance metadata is also needed to be
	// counted. For every useful node, we track 2 extra hashes as the flushlist.
	size := db.nodesSize + common.StorageSize((len(db.nodes)-1)*2*common.HashLength)

	// If the preimage cache got large enough, push to disk. If it's still small
	// leave for later to deduplicate writes.
	flushPreimages := db.preimagesSize > 4*1024*1024
	if flushPreimages {
		for hash, preimage := range db.preimages {
			if err := batch.Put(db.secureKey(hash[:]), preimage); err != nil {
				log4j.Error("Failed to commit preimage from trie database", "err", err)
				db.lock.RUnlock()
				return err
			}
			if batch.ValueSize() > candb.IdealBatchSize {
				if err := batch.Write(); err != nil {
					db.lock.RUnlock()
					return err
				}
				batch.Reset()
			}
		}
	}
	// Keep committing nodes from the flush-list until we're below allowance
	oldest := db.oldest
	for size > limit && oldest != (common.Hash{}) {
		// Fetch the oldest referenced node and push into the batch
		node := db.nodes[oldest]
		if err := batch.Put(oldest[:], node.rlp()); err != nil {
			db.lock.RUnlock()
			return err
		}
		// If we exceeded the ideal batch size, commit and reset
		if batch.ValueSize() >= candb.IdealBatchSize {
			if err := batch.Write(); err != nil {
				log4j.Error("Failed to write flush list to disk", "err", err)
				db.lock.RUnlock()
				return err
			}
			batch.Reset()
		}
		// Iterate to the next flush item, or abort if the size cap was achieved. Size
		// is the total size, including both the useful cached data (hash -> blob), as
		// well as the flushlist metadata (2*hash). When flushing items from the cache,
		// we need to reduce both.
		size -= common.StorageSize(3*common.HashLength + int(node.size))
		oldest = node.flushNext
	}
	// Flush out any remainder data from the last batch
	if err := batch.Write(); err != nil {
		log4j.Error("Failed to write flush list to disk", "err", err)
		db.lock.RUnlock()
		return err
	}
	db.lock.RUnlock()

	// Write successful, clear out the flushed data
	db.lock.Lock()
	defer db.lock.Unlock()

	if flushPreimages {
		db.preimages = make(map[common.Hash][]byte)
		db.preimagesSize = 0
	}
	for db.oldest != oldest {
		node := db.nodes[db.oldest]
		delete(db.nodes, db.oldest)
		db.oldest = node.flushNext

		db.nodesSize -= common.StorageSize(common.HashLength + int(node.size))
	}
	if db.oldest != (common.Hash{}) {
		db.nodes[db.oldest].flushPrev = common.Hash{}
	}
	db.flushnodes += uint64(nodes - len(db.nodes))
	db.flushsize += storage - db.nodesSize
	db.flushtime += time.Since(start)

	memcacheFlushTimeTimer.Update(time.Since(start))
	memcacheFlushSizeMeter.Mark(int64(storage - db.nodesSize))
	memcacheFlushNodesMeter.Mark(int64(nodes - len(db.nodes)))

	log4j.Debug("Persisted nodes from memory database", "nodes", nodes-len(db.nodes), "size", storage-db.nodesSize, "time", time.Since(start),
		"flushnodes", db.flushnodes, "flushsize", db.flushsize, "flushtime", db.flushtime, "livenodes", len(db.nodes), "livesize", db.nodesSize)

	return nil
}

//
func (db *Database) Commit(node common.Hash, report bool) error {
	// Create a database batch to flush persistent data out. It is important that
	// outside code doesn't see an inconsistent state (referenced data removed from
	// memory cache during commit but not yet in persistent storage). This is ensured
	// by only uncaching existing data when the database write finalizes.
	db.lock.RLock()

	start := time.Now()
	batch := db.diskdb.NewBatch()

	// Move all of the accumulated preimages into a write batch
	for hash, preimage := range db.preimages {
		if err := batch.Put(db.secureKey(hash[:]), preimage); err != nil {
			log4j.Error("Failed to commit preimage from trie database", "err", err)
			db.lock.RUnlock()
			return err
		}
		if batch.ValueSize() > candb.IdealBatchSize {
			if err := batch.Write(); err != nil {
				return err
			}
			batch.Reset()
		}
	}
	// Move the trie itself into the batch, flushing if enough data is accumulated
	nodes, storage := len(db.nodes), db.nodesSize
	if err := db.commit(node, batch); err != nil {
		log4j.Error("Failed to commit trie from trie database", "err", err)
		db.lock.RUnlock()
		return err
	}
	// Write batch ready, unlock for readers during persistence
	if err := batch.Write(); err != nil {
		log4j.Error("Failed to write trie to disk", "err", err)
		db.lock.RUnlock()
		return err
	}
	db.lock.RUnlock()

	// Write successful, clear out the flushed data
	db.lock.Lock()
	defer db.lock.Unlock()

	db.preimages = make(map[common.Hash][]byte)
	db.preimagesSize = 0

	db.uncache(node)

	memcacheCommitTimeTimer.Update(time.Since(start))
	memcacheCommitSizeMeter.Mark(int64(storage - db.nodesSize))
	memcacheCommitNodesMeter.Mark(int64(nodes - len(db.nodes)))

	logger := log4j.Info
	if !report {
		logger = log4j.Debug
	}
	logger("Persisted trie from memory database", "nodes", nodes-len(db.nodes)+int(db.flushnodes), "size", storage-db.nodesSize+db.flushsize, "time", time.Since(start)+db.flushtime,
		"gcnodes", db.gcnodes, "gcsize", db.gcsize, "gctime", db.gctime, "livenodes", len(db.nodes), "livesize", db.nodesSize)

	// Reset the garbage collection statistics
	db.gcnodes, db.gcsize, db.gctime = 0, 0, 0
	db.flushnodes, db.flushsize, db.flushtime = 0, 0, 0

	return nil
}

func (db *Database) commit(hash common.Hash, batch candb.Batch) error {
	// If the node does not exist, it's a previously committed node
	node, ok := db.nodes[hash]
	if !ok {
		return nil
	}
	for _, child := range node.childs() {
		if err := db.commit(child, batch); err != nil {
			return err
		}
	}
	if err := batch.Put(hash[:], node.rlp()); err != nil {
		return err
	}
	// If we've reached an optimal batch size, commit and start over
	if batch.ValueSize() >= candb.IdealBatchSize {
		if err := batch.Write(); err != nil {
			return err
		}
		batch.Reset()
	}
	return nil
}

func (db *Database) uncache(hash common.Hash) {
	// If the node does not exist, we're done on this path
	node, ok := db.nodes[hash]
	if !ok {
		return
	}
	// Node still exists, remove it from the flush-list
	if hash == db.oldest {
		db.oldest = node.flushNext
	} else {
		db.nodes[node.flushPrev].flushNext = node.flushNext
		db.nodes[node.flushNext].flushPrev = node.flushPrev
	}
	// Uncache the node's subtries and remove the node itself too
	for _, child := range node.childs() {
		db.uncache(child)
	}
	delete(db.nodes, hash)
	db.nodesSize -= common.StorageSize(common.HashLength + int(node.size))
}

func (db *Database) Size() (common.StorageSize, common.StorageSize) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	// db.nodesSize only contains the useful data in the cache, but when reporting
	// the total memory consumption, the maintenance metadata is also needed to be
	// counted. For every useful node, we track 2 extra hashes as the flushlist.
	var flushlistSize = common.StorageSize((len(db.nodes) - 1) * 2 * common.HashLength)
	return db.nodesSize + flushlistSize, db.preimagesSize
}
