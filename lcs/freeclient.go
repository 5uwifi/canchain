package lcs

import (
	"io"
	"math"
	"sync"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common/mclock"
	"github.com/5uwifi/canchain/common/prque"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/rlp"
)

type freeClientPool struct {
	db     candb.Database
	lock   sync.Mutex
	clock  mclock.Clock
	closed bool

	connectedLimit, totalLimit int

	addressMap            map[string]*freeClientPoolEntry
	connPool, disconnPool *prque.Prque
	startupTime           mclock.AbsTime
	logOffsetAtStartup    int64
}

const (
	recentUsageExpTC     = time.Hour
	fixedPointMultiplier = 0x1000000
	connectedBias        = time.Minute
)

func newFreeClientPool(db candb.Database, connectedLimit, totalLimit int, clock mclock.Clock) *freeClientPool {
	pool := &freeClientPool{
		db:             db,
		clock:          clock,
		addressMap:     make(map[string]*freeClientPoolEntry),
		connPool:       prque.New(poolSetIndex),
		disconnPool:    prque.New(poolSetIndex),
		connectedLimit: connectedLimit,
		totalLimit:     totalLimit,
	}
	pool.loadFromDb()
	return pool
}

func (f *freeClientPool) stop() {
	f.lock.Lock()
	f.closed = true
	f.saveToDb()
	f.lock.Unlock()
}

func (f *freeClientPool) connect(address string, disconnectFn func()) bool {
	f.lock.Lock()
	defer f.lock.Unlock()

	if f.closed {
		return false
	}
	e := f.addressMap[address]
	now := f.clock.Now()
	var recentUsage int64
	if e == nil {
		e = &freeClientPoolEntry{address: address, index: -1}
		f.addressMap[address] = e
	} else {
		if e.connected {
			log4j.Debug("Client already connected", "address", address)
			return false
		}
		recentUsage = int64(math.Exp(float64(e.logUsage-f.logOffset(now)) / fixedPointMultiplier))
	}
	e.linUsage = recentUsage - int64(now)
	if f.connPool.Size() == f.connectedLimit {
		i := f.connPool.PopItem().(*freeClientPoolEntry)
		if e.linUsage+int64(connectedBias)-i.linUsage < 0 {
			f.connPool.Remove(i.index)
			f.calcLogUsage(i, now)
			i.connected = false
			f.disconnPool.Push(i, -i.logUsage)
			log4j.Debug("Client kicked out", "address", i.address)
			i.disconnectFn()
		} else {
			f.connPool.Push(i, i.linUsage)
			log4j.Debug("Client rejected", "address", address)
			return false
		}
	}
	f.disconnPool.Remove(e.index)
	e.connected = true
	e.disconnectFn = disconnectFn
	f.connPool.Push(e, e.linUsage)
	if f.connPool.Size()+f.disconnPool.Size() > f.totalLimit {
		f.disconnPool.Pop()
	}
	log4j.Debug("Client accepted", "address", address)
	return true
}

func (f *freeClientPool) disconnect(address string) {
	f.lock.Lock()
	defer f.lock.Unlock()

	if f.closed {
		return
	}
	e := f.addressMap[address]
	now := f.clock.Now()
	if !e.connected {
		log4j.Debug("Client already disconnected", "address", address)
		return
	}

	f.connPool.Remove(e.index)
	f.calcLogUsage(e, now)
	e.connected = false
	f.disconnPool.Push(e, -e.logUsage)
	log4j.Debug("Client disconnected", "address", address)
}

func (f *freeClientPool) logOffset(now mclock.AbsTime) int64 {
	logDecay := int64((time.Duration(now - f.startupTime)) / (recentUsageExpTC / fixedPointMultiplier))
	return f.logOffsetAtStartup + logDecay
}

func (f *freeClientPool) calcLogUsage(e *freeClientPoolEntry, now mclock.AbsTime) {
	dt := e.linUsage + int64(now)
	if dt < 1 {
		dt = 1
	}
	e.logUsage = int64(math.Log(float64(dt))*fixedPointMultiplier) + f.logOffset(now)
}

type freeClientPoolStorage struct {
	LogOffset uint64
	List      []*freeClientPoolEntry
}

func (f *freeClientPool) loadFromDb() {
	enc, err := f.db.Get([]byte("freeClientPool"))
	if err != nil {
		return
	}
	var storage freeClientPoolStorage
	err = rlp.DecodeBytes(enc, &storage)
	if err != nil {
		log4j.Error("Failed to decode client list", "err", err)
		return
	}
	f.logOffsetAtStartup = int64(storage.LogOffset)
	f.startupTime = f.clock.Now()
	for _, e := range storage.List {
		log4j.Debug("Loaded free client record", "address", e.address, "logUsage", e.logUsage)
		f.addressMap[e.address] = e
		f.disconnPool.Push(e, -e.logUsage)
	}
}

func (f *freeClientPool) saveToDb() {
	now := f.clock.Now()
	storage := freeClientPoolStorage{
		LogOffset: uint64(f.logOffset(now)),
		List:      make([]*freeClientPoolEntry, len(f.addressMap)),
	}
	i := 0
	for _, e := range f.addressMap {
		if e.connected {
			f.calcLogUsage(e, now)
		}
		storage.List[i] = e
		i++
	}
	enc, err := rlp.EncodeToBytes(storage)
	if err != nil {
		log4j.Error("Failed to encode client list", "err", err)
	} else {
		f.db.Put([]byte("freeClientPool"), enc)
	}
}

type freeClientPoolEntry struct {
	address            string
	connected          bool
	disconnectFn       func()
	linUsage, logUsage int64
	index              int
}

func (e *freeClientPoolEntry) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, []interface{}{e.address, uint64(e.logUsage)})
}

func (e *freeClientPoolEntry) DecodeRLP(s *rlp.Stream) error {
	var entry struct {
		Address  string
		LogUsage uint64
	}
	if err := s.Decode(&entry); err != nil {
		return err
	}
	e.address = entry.Address
	e.logUsage = int64(entry.LogUsage)
	e.connected = false
	e.index = -1
	return nil
}

func poolSetIndex(a interface{}, i int) {
	a.(*freeClientPoolEntry).index = i
}
