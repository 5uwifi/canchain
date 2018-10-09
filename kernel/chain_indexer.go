package kernel

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
)

type ChainIndexerBackend interface {
	Reset(ctx context.Context, section uint64, prevHead common.Hash) error

	Process(ctx context.Context, header *types.Header) error

	Commit() error
}

type ChainIndexerChain interface {
	CurrentHeader() *types.Header

	SubscribeChainHeadEvent(ch chan<- ChainHeadEvent) event.Subscription
}

type ChainIndexer struct {
	chainDb  candb.Database
	indexDb  candb.Database
	backend  ChainIndexerBackend
	children []*ChainIndexer

	active    uint32
	update    chan struct{}
	quit      chan chan error
	ctx       context.Context
	ctxCancel func()

	sectionSize uint64
	confirmsReq uint64

	storedSections uint64
	knownSections  uint64
	cascadedHead   uint64

	checkpointSections uint64
	checkpointHead     common.Hash

	throttling time.Duration

	log  log4j.Logger
	lock sync.RWMutex
}

func NewChainIndexer(chainDb, indexDb candb.Database, backend ChainIndexerBackend, section, confirm uint64, throttling time.Duration, kind string) *ChainIndexer {
	c := &ChainIndexer{
		chainDb:     chainDb,
		indexDb:     indexDb,
		backend:     backend,
		update:      make(chan struct{}, 1),
		quit:        make(chan chan error),
		sectionSize: section,
		confirmsReq: confirm,
		throttling:  throttling,
		log:         log4j.New("type", kind),
	}
	c.loadValidSections()
	c.ctx, c.ctxCancel = context.WithCancel(context.Background())

	go c.updateLoop()

	return c
}

func (c *ChainIndexer) AddCheckpoint(section uint64, shead common.Hash) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.checkpointSections = section + 1
	c.checkpointHead = shead

	if section < c.storedSections {
		return
	}
	c.setSectionHead(section, shead)
	c.setValidSections(section + 1)
}

func (c *ChainIndexer) Start(chain ChainIndexerChain) {
	events := make(chan ChainHeadEvent, 10)
	sub := chain.SubscribeChainHeadEvent(events)

	go c.eventLoop(chain.CurrentHeader(), events, sub)
}

func (c *ChainIndexer) Close() error {
	var errs []error

	c.ctxCancel()

	errc := make(chan error)
	c.quit <- errc
	if err := <-errc; err != nil {
		errs = append(errs, err)
	}
	if atomic.LoadUint32(&c.active) != 0 {
		c.quit <- errc
		if err := <-errc; err != nil {
			errs = append(errs, err)
		}
	}
	for _, child := range c.children {
		if err := child.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	switch {
	case len(errs) == 0:
		return nil

	case len(errs) == 1:
		return errs[0]

	default:
		return fmt.Errorf("%v", errs)
	}
}

func (c *ChainIndexer) eventLoop(currentHeader *types.Header, events chan ChainHeadEvent, sub event.Subscription) {
	atomic.StoreUint32(&c.active, 1)

	defer sub.Unsubscribe()

	c.newHead(currentHeader.Number.Uint64(), false)

	var (
		prevHeader = currentHeader
		prevHash   = currentHeader.Hash()
	)
	for {
		select {
		case errc := <-c.quit:
			errc <- nil
			return

		case ev, ok := <-events:
			if !ok {
				errc := <-c.quit
				errc <- nil
				return
			}
			header := ev.Block.Header()
			if header.ParentHash != prevHash {

				if rawdb.ReadCanonicalHash(c.chainDb, prevHeader.Number.Uint64()) != prevHash {
					if h := rawdb.FindCommonAncestor(c.chainDb, prevHeader, header); h != nil {
						c.newHead(h.Number.Uint64(), true)
					}
				}
			}
			c.newHead(header.Number.Uint64(), false)

			prevHeader, prevHash = header, header.Hash()
		}
	}
}

func (c *ChainIndexer) newHead(head uint64, reorg bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if reorg {
		known := head / c.sectionSize
		stored := known
		if known < c.checkpointSections {
			known = 0
		}
		if stored < c.checkpointSections {
			stored = c.checkpointSections
		}
		if known < c.knownSections {
			c.knownSections = known
		}
		if stored < c.storedSections {
			c.setValidSections(stored)
		}
		head = known * c.sectionSize

		if head < c.cascadedHead {
			c.cascadedHead = head
			for _, child := range c.children {
				child.newHead(c.cascadedHead, true)
			}
		}
		return
	}
	var sections uint64
	if head >= c.confirmsReq {
		sections = (head + 1 - c.confirmsReq) / c.sectionSize
		if sections < c.checkpointSections {
			sections = 0
		}
		if sections > c.knownSections {
			if c.knownSections < c.checkpointSections {
				syncedHead := rawdb.ReadCanonicalHash(c.chainDb, c.checkpointSections*c.sectionSize-1)
				if syncedHead != c.checkpointHead {
					c.log.Error("Synced chain does not match checkpoint", "number", c.checkpointSections*c.sectionSize-1, "expected", c.checkpointHead, "synced", syncedHead)
					return
				}
			}
			c.knownSections = sections

			select {
			case c.update <- struct{}{}:
			default:
			}
		}
	}
}

func (c *ChainIndexer) updateLoop() {
	var (
		updating bool
		updated  time.Time
	)

	for {
		select {
		case errc := <-c.quit:
			errc <- nil
			return

		case <-c.update:
			c.lock.Lock()
			if c.knownSections > c.storedSections {
				if time.Since(updated) > 8*time.Second {
					if c.knownSections > c.storedSections+1 {
						updating = true
						c.log.Info("Upgrading chain index", "percentage", c.storedSections*100/c.knownSections)
					}
					updated = time.Now()
				}
				section := c.storedSections
				var oldHead common.Hash
				if section > 0 {
					oldHead = c.SectionHead(section - 1)
				}
				c.lock.Unlock()
				newHead, err := c.processSection(section, oldHead)
				if err != nil {
					select {
					case <-c.ctx.Done():
						<-c.quit <- nil
						return
					default:
					}
					c.log.Error("Section processing failed", "error", err)
				}
				c.lock.Lock()

				if err == nil && oldHead == c.SectionHead(section-1) {
					c.setSectionHead(section, newHead)
					c.setValidSections(section + 1)
					if c.storedSections == c.knownSections && updating {
						updating = false
						c.log.Info("Finished upgrading chain index")
					}
					c.cascadedHead = c.storedSections*c.sectionSize - 1
					for _, child := range c.children {
						c.log.Trace("Cascading chain index update", "head", c.cascadedHead)
						child.newHead(c.cascadedHead, false)
					}
				} else {
					c.log.Debug("Chain index processing failed", "section", section, "err", err)
					c.knownSections = c.storedSections
				}
			}
			if c.knownSections > c.storedSections {
				time.AfterFunc(c.throttling, func() {
					select {
					case c.update <- struct{}{}:
					default:
					}
				})
			}
			c.lock.Unlock()
		}
	}
}

func (c *ChainIndexer) processSection(section uint64, lastHead common.Hash) (common.Hash, error) {
	c.log.Trace("Processing new chain section", "section", section)

	if err := c.backend.Reset(c.ctx, section, lastHead); err != nil {
		c.setValidSections(0)
		return common.Hash{}, err
	}

	for number := section * c.sectionSize; number < (section+1)*c.sectionSize; number++ {
		hash := rawdb.ReadCanonicalHash(c.chainDb, number)
		if hash == (common.Hash{}) {
			return common.Hash{}, fmt.Errorf("canonical block #%d unknown", number)
		}
		header := rawdb.ReadHeader(c.chainDb, hash, number)
		if header == nil {
			return common.Hash{}, fmt.Errorf("block #%d [%x…] not found", number, hash[:4])
		} else if header.ParentHash != lastHead {
			return common.Hash{}, fmt.Errorf("chain reorged during section processing")
		}
		if err := c.backend.Process(c.ctx, header); err != nil {
			return common.Hash{}, err
		}
		lastHead = header.Hash()
	}
	if err := c.backend.Commit(); err != nil {
		return common.Hash{}, err
	}
	return lastHead, nil
}

func (c *ChainIndexer) Sections() (uint64, uint64, common.Hash) {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.storedSections, c.storedSections*c.sectionSize - 1, c.SectionHead(c.storedSections - 1)
}

func (c *ChainIndexer) AddChildIndexer(indexer *ChainIndexer) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.children = append(c.children, indexer)

	sections := c.storedSections
	if c.knownSections < sections {
		sections = c.knownSections
	}
	if sections > 0 {
		indexer.newHead(sections*c.sectionSize-1, false)
	}
}

func (c *ChainIndexer) loadValidSections() {
	data, _ := c.indexDb.Get([]byte("count"))
	if len(data) == 8 {
		c.storedSections = binary.BigEndian.Uint64(data)
	}
}

func (c *ChainIndexer) setValidSections(sections uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], sections)
	c.indexDb.Put([]byte("count"), data[:])

	for c.storedSections > sections {
		c.storedSections--
		c.removeSectionHead(c.storedSections)
	}
	c.storedSections = sections
}

func (c *ChainIndexer) SectionHead(section uint64) common.Hash {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], section)

	hash, _ := c.indexDb.Get(append([]byte("shead"), data[:]...))
	if len(hash) == len(common.Hash{}) {
		return common.BytesToHash(hash)
	}
	return common.Hash{}
}

func (c *ChainIndexer) setSectionHead(section uint64, hash common.Hash) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], section)

	c.indexDb.Put(append([]byte("shead"), data[:]...), hash.Bytes())
}

func (c *ChainIndexer) removeSectionHead(section uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], section)

	c.indexDb.Delete(append([]byte("shead"), data[:]...))
}
