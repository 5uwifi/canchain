//
// (at your option) any later version.
//
//

//
//
//  - db - LevelDB backend
//  - mem - in memory map backend
//  - rpc - RPC client that can connect to other backends
//
package mock

import (
	"errors"
	"io"

	"github.com/5uwifi/canchain/common"
)

var ErrNotFound = errors.New("not found")

type NodeStore struct {
	store GlobalStorer
	addr  common.Address
}

func NewNodeStore(addr common.Address, store GlobalStorer) *NodeStore {
	return &NodeStore{
		store: store,
		addr:  addr,
	}
}

func (n *NodeStore) Get(key []byte) (data []byte, err error) {
	return n.store.Get(n.addr, key)
}

func (n *NodeStore) Put(key []byte, data []byte) error {
	return n.store.Put(n.addr, key, data)
}

type GlobalStorer interface {
	Get(addr common.Address, key []byte) (data []byte, err error)
	Put(addr common.Address, key []byte, data []byte) error
	HasKey(addr common.Address, key []byte) bool
	// NewNodeStore creates an instance of NodeStore
	// to be used by a single swarm node with
	// address addr.
	NewNodeStore(addr common.Address) *NodeStore
}

type Importer interface {
	Import(r io.Reader) (n int, err error)
}

type Exporter interface {
	Export(w io.Writer) (n int, err error)
}

type ImportExporter interface {
	Importer
	Exporter
}

type ExportedChunk struct {
	Data  []byte           `json:"d"`
	Addrs []common.Address `json:"a"`
}
