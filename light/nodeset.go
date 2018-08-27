package light

import (
	"errors"
	"sync"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/rlp"
)

type NodeSet struct {
	nodes map[string][]byte
	order []string

	dataSize int
	lock     sync.RWMutex
}

func NewNodeSet() *NodeSet {
	return &NodeSet{
		nodes: make(map[string][]byte),
	}
}

func (db *NodeSet) Put(key []byte, value []byte) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	if _, ok := db.nodes[string(key)]; ok {
		return nil
	}
	keystr := string(key)

	db.nodes[keystr] = common.CopyBytes(value)
	db.order = append(db.order, keystr)
	db.dataSize += len(value)

	return nil
}

func (db *NodeSet) Get(key []byte) ([]byte, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	if entry, ok := db.nodes[string(key)]; ok {
		return entry, nil
	}
	return nil, errors.New("not found")
}

func (db *NodeSet) Has(key []byte) (bool, error) {
	_, err := db.Get(key)
	return err == nil, nil
}

func (db *NodeSet) KeyCount() int {
	db.lock.RLock()
	defer db.lock.RUnlock()

	return len(db.nodes)
}

func (db *NodeSet) DataSize() int {
	db.lock.RLock()
	defer db.lock.RUnlock()

	return db.dataSize
}

func (db *NodeSet) NodeList() NodeList {
	db.lock.RLock()
	defer db.lock.RUnlock()

	var values NodeList
	for _, key := range db.order {
		values = append(values, db.nodes[key])
	}
	return values
}

func (db *NodeSet) Store(target candb.Putter) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	for key, value := range db.nodes {
		target.Put([]byte(key), value)
	}
}

type NodeList []rlp.RawValue

func (n NodeList) Store(db candb.Putter) {
	for _, node := range n {
		db.Put(crypto.Keccak256(node), node)
	}
}

func (n NodeList) NodeSet() *NodeSet {
	db := NewNodeSet()
	n.Store(db)
	return db
}

func (n *NodeList) Put(key []byte, value []byte) error {
	*n = append(*n, value)
	return nil
}

func (n NodeList) DataSize() int {
	var size int
	for _, node := range n {
		size += len(node)
	}
	return size
}
