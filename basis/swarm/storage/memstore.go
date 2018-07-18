

package storage

import (
	"sync"

	lru "github.com/hashicorp/golang-lru"
)

type MemStore struct {
	cache    *lru.Cache
	requests *lru.Cache
	mu       sync.RWMutex
	disabled bool
}

//
//`requests` LRU cache capacity should ideally never be reached, this is why for the time being it should be initialised
func NewMemStore(params *StoreParams, _ *LDBStore) (m *MemStore) {
	if params.CacheCapacity == 0 {
		return &MemStore{
			disabled: true,
		}
	}

	onEvicted := func(key interface{}, value interface{}) {
		v := value.(*Chunk)
		<-v.dbStoredC
	}
	c, err := lru.NewWithEvict(int(params.CacheCapacity), onEvicted)
	if err != nil {
		panic(err)
	}

	requestEvicted := func(key interface{}, value interface{}) {
		// temporary remove of the error log, until we figure out the problem, as it is too spamy
		//log.Error("evict called on outgoing request")
	}
	r, err := lru.NewWithEvict(int(params.ChunkRequestsCacheCapacity), requestEvicted)
	if err != nil {
		panic(err)
	}

	return &MemStore{
		cache:    c,
		requests: r,
	}
}

func (m *MemStore) Get(addr Address) (*Chunk, error) {
	if m.disabled {
		return nil, ErrChunkNotFound
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	r, ok := m.requests.Get(string(addr))
	// it is a request
	if ok {
		return r.(*Chunk), nil
	}

	// it is not a request
	c, ok := m.cache.Get(string(addr))
	if !ok {
		return nil, ErrChunkNotFound
	}
	return c.(*Chunk), nil
}

func (m *MemStore) Put(c *Chunk) {
	if m.disabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// it is a request
	if c.ReqC != nil {
		select {
		case <-c.ReqC:
			if c.GetErrored() != nil {
				m.requests.Remove(string(c.Addr))
				return
			}
			m.cache.Add(string(c.Addr), c)
			m.requests.Remove(string(c.Addr))
		default:
			m.requests.Add(string(c.Addr), c)
		}
		return
	}

	// it is not a request
	m.cache.Add(string(c.Addr), c)
	m.requests.Remove(string(c.Addr))
}

func (m *MemStore) setCapacity(n int) {
	if n <= 0 {
		m.disabled = true
	} else {
		onEvicted := func(key interface{}, value interface{}) {
			v := value.(*Chunk)
			<-v.dbStoredC
		}
		c, err := lru.NewWithEvict(n, onEvicted)
		if err != nil {
			panic(err)
		}

		r, err := lru.New(defaultChunkRequestsCacheCapacity)
		if err != nil {
			panic(err)
		}

		m = &MemStore{
			cache:    c,
			requests: r,
		}
	}
}

func (s *MemStore) Close() {}
