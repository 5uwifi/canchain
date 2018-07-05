//
// (at your option) any later version.
//
//

//
package rpc

import (
	"fmt"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/rpc"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/storage/mock"
)

type GlobalStore struct {
	client *rpc.Client
}

func NewGlobalStore(client *rpc.Client) *GlobalStore {
	return &GlobalStore{
		client: client,
	}
}

func (s *GlobalStore) Close() error {
	s.client.Close()
	return nil
}

func (s *GlobalStore) NewNodeStore(addr common.Address) *mock.NodeStore {
	return mock.NewNodeStore(addr, s)
}

func (s *GlobalStore) Get(addr common.Address, key []byte) (data []byte, err error) {
	err = s.client.Call(&data, "mockStore_get", addr, key)
	if err != nil && err.Error() == "not found" {
		// pass the mock package value of error instead an rpc error
		return data, mock.ErrNotFound
	}
	return data, err
}

func (s *GlobalStore) Put(addr common.Address, key []byte, data []byte) error {
	err := s.client.Call(nil, "mockStore_put", addr, key, data)
	return err
}

func (s *GlobalStore) HasKey(addr common.Address, key []byte) bool {
	var has bool
	if err := s.client.Call(&has, "mockStore_hasKey", addr, key); err != nil {
		log.Error(fmt.Sprintf("mock store HasKey: addr %s, key %064x: %v", addr, key, err))
		return false
	}
	return has
}
