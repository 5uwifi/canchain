package light

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/params"
	"github.com/hashicorp/golang-lru"
)

var (
	bodyCacheLimit  = 256
	blockCacheLimit = 256
)

type LightChain struct {
	hc            *kernel.HeaderChain
	chainDb       candb.Database
	odr           OdrBackend
	chainFeed     event.Feed
	chainSideFeed event.Feed
	chainHeadFeed event.Feed
	scope         event.SubscriptionScope
	genesisBlock  *types.Block

	mu      sync.RWMutex
	chainmu sync.RWMutex

	bodyCache    *lru.Cache
	bodyRLPCache *lru.Cache
	blockCache   *lru.Cache

	quit          chan struct{}
	running       int32
	procInterrupt int32
	wg            sync.WaitGroup

	engine consensus.Engine
}

func NewLightChain(odr OdrBackend, config *params.ChainConfig, engine consensus.Engine) (*LightChain, error) {
	bodyCache, _ := lru.New(bodyCacheLimit)
	bodyRLPCache, _ := lru.New(bodyCacheLimit)
	blockCache, _ := lru.New(blockCacheLimit)

	bc := &LightChain{
		chainDb:      odr.Database(),
		odr:          odr,
		quit:         make(chan struct{}),
		bodyCache:    bodyCache,
		bodyRLPCache: bodyRLPCache,
		blockCache:   blockCache,
		engine:       engine,
	}
	var err error
	bc.hc, err = kernel.NewHeaderChain(odr.Database(), config, bc.engine, bc.getProcInterrupt)
	if err != nil {
		return nil, err
	}
	bc.genesisBlock, _ = bc.GetBlockByNumber(NoOdr, 0)
	if bc.genesisBlock == nil {
		return nil, kernel.ErrNoGenesis
	}
	if cp, ok := trustedCheckpoints[bc.genesisBlock.Hash()]; ok {
		bc.addTrustedCheckpoint(cp)
	}
	if err := bc.loadLastState(); err != nil {
		return nil, err
	}
	for hash := range kernel.BadHashes {
		if header := bc.GetHeaderByHash(hash); header != nil {
			log4j.Error("Found bad hash, rewinding chain", "number", header.Number, "hash", header.ParentHash)
			bc.SetHead(header.Number.Uint64() - 1)
			log4j.Error("Chain rewind was successful, resuming normal operation")
		}
	}
	return bc, nil
}

func (self *LightChain) addTrustedCheckpoint(cp TrustedCheckpoint) {
	if self.odr.ChtIndexer() != nil {
		StoreChtRoot(self.chainDb, cp.SectionIdx, cp.SectionHead, cp.CHTRoot)
		self.odr.ChtIndexer().AddKnownSectionHead(cp.SectionIdx, cp.SectionHead)
	}
	if self.odr.BloomTrieIndexer() != nil {
		StoreBloomTrieRoot(self.chainDb, cp.SectionIdx, cp.SectionHead, cp.BloomRoot)
		self.odr.BloomTrieIndexer().AddKnownSectionHead(cp.SectionIdx, cp.SectionHead)
	}
	if self.odr.BloomIndexer() != nil {
		self.odr.BloomIndexer().AddKnownSectionHead(cp.SectionIdx, cp.SectionHead)
	}
	log4j.Info("Added trusted checkpoint", "chain", cp.name, "block", (cp.SectionIdx+1)*CHTFrequencyClient-1, "hash", cp.SectionHead)
}

func (self *LightChain) getProcInterrupt() bool {
	return atomic.LoadInt32(&self.procInterrupt) == 1
}

func (self *LightChain) Odr() OdrBackend {
	return self.odr
}

func (self *LightChain) loadLastState() error {
	if head := rawdb.ReadHeadHeaderHash(self.chainDb); head == (common.Hash{}) {
		self.Reset()
	} else {
		if header := self.GetHeaderByHash(head); header != nil {
			self.hc.SetCurrentHeader(header)
		}
	}

	header := self.hc.CurrentHeader()
	headerTd := self.GetTd(header.Hash(), header.Number.Uint64())
	log4j.Info("Loaded most recent local header", "number", header.Number, "hash", header.Hash(), "td", headerTd)

	return nil
}

func (bc *LightChain) SetHead(head uint64) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.hc.SetHead(head, nil)
	bc.loadLastState()
}

func (self *LightChain) GasLimit() uint64 {
	return self.hc.CurrentHeader().GasLimit
}

func (bc *LightChain) Reset() {
	bc.ResetWithGenesisBlock(bc.genesisBlock)
}

func (bc *LightChain) ResetWithGenesisBlock(genesis *types.Block) {
	bc.SetHead(0)

	bc.mu.Lock()
	defer bc.mu.Unlock()

	rawdb.WriteTd(bc.chainDb, genesis.Hash(), genesis.NumberU64(), genesis.Difficulty())
	rawdb.WriteBlock(bc.chainDb, genesis)

	bc.genesisBlock = genesis
	bc.hc.SetGenesis(bc.genesisBlock.Header())
	bc.hc.SetCurrentHeader(bc.genesisBlock.Header())
}

func (bc *LightChain) Engine() consensus.Engine { return bc.engine }

func (bc *LightChain) Genesis() *types.Block {
	return bc.genesisBlock
}

func (bc *LightChain) State() (*state.StateDB, error) {
	return nil, errors.New("not implemented, needs client/server interface split")
}

func (self *LightChain) GetBody(ctx context.Context, hash common.Hash) (*types.Body, error) {
	if cached, ok := self.bodyCache.Get(hash); ok {
		body := cached.(*types.Body)
		return body, nil
	}
	number := self.hc.GetBlockNumber(hash)
	if number == nil {
		return nil, errors.New("unknown block")
	}
	body, err := GetBody(ctx, self.odr, hash, *number)
	if err != nil {
		return nil, err
	}
	self.bodyCache.Add(hash, body)
	return body, nil
}

func (self *LightChain) GetBodyRLP(ctx context.Context, hash common.Hash) (rlp.RawValue, error) {
	if cached, ok := self.bodyRLPCache.Get(hash); ok {
		return cached.(rlp.RawValue), nil
	}
	number := self.hc.GetBlockNumber(hash)
	if number == nil {
		return nil, errors.New("unknown block")
	}
	body, err := GetBodyRLP(ctx, self.odr, hash, *number)
	if err != nil {
		return nil, err
	}
	self.bodyRLPCache.Add(hash, body)
	return body, nil
}

func (bc *LightChain) HasBlock(hash common.Hash, number uint64) bool {
	blk, _ := bc.GetBlock(NoOdr, hash, number)
	return blk != nil
}

func (self *LightChain) GetBlock(ctx context.Context, hash common.Hash, number uint64) (*types.Block, error) {
	if block, ok := self.blockCache.Get(hash); ok {
		return block.(*types.Block), nil
	}
	block, err := GetBlock(ctx, self.odr, hash, number)
	if err != nil {
		return nil, err
	}
	self.blockCache.Add(block.Hash(), block)
	return block, nil
}

func (self *LightChain) GetBlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	number := self.hc.GetBlockNumber(hash)
	if number == nil {
		return nil, errors.New("unknown block")
	}
	return self.GetBlock(ctx, hash, *number)
}

func (self *LightChain) GetBlockByNumber(ctx context.Context, number uint64) (*types.Block, error) {
	hash, err := GetCanonicalHash(ctx, self.odr, number)
	if hash == (common.Hash{}) || err != nil {
		return nil, err
	}
	return self.GetBlock(ctx, hash, number)
}

func (bc *LightChain) Stop() {
	if !atomic.CompareAndSwapInt32(&bc.running, 0, 1) {
		return
	}
	close(bc.quit)
	atomic.StoreInt32(&bc.procInterrupt, 1)

	bc.wg.Wait()
	log4j.Info("Blockchain manager stopped")
}

func (self *LightChain) Rollback(chain []common.Hash) {
	self.mu.Lock()
	defer self.mu.Unlock()

	for i := len(chain) - 1; i >= 0; i-- {
		hash := chain[i]

		if head := self.hc.CurrentHeader(); head.Hash() == hash {
			self.hc.SetCurrentHeader(self.GetHeader(head.ParentHash, head.Number.Uint64()-1))
		}
	}
}

func (self *LightChain) postChainEvents(events []interface{}) {
	for _, event := range events {
		switch ev := event.(type) {
		case kernel.ChainEvent:
			if self.CurrentHeader().Hash() == ev.Hash {
				self.chainHeadFeed.Send(kernel.ChainHeadEvent{Block: ev.Block})
			}
			self.chainFeed.Send(ev)
		case kernel.ChainSideEvent:
			self.chainSideFeed.Send(ev)
		}
	}
}

func (self *LightChain) InsertHeaderChain(chain []*types.Header, checkFreq int) (int, error) {
	start := time.Now()
	if i, err := self.hc.ValidateHeaderChain(chain, checkFreq); err != nil {
		return i, err
	}

	self.chainmu.Lock()
	defer func() {
		self.chainmu.Unlock()
		time.Sleep(time.Millisecond * 10)
	}()

	self.wg.Add(1)
	defer self.wg.Done()

	var events []interface{}
	whFunc := func(header *types.Header) error {
		self.mu.Lock()
		defer self.mu.Unlock()

		status, err := self.hc.WriteHeader(header)

		switch status {
		case kernel.CanonStatTy:
			log4j.Debug("Inserted new header", "number", header.Number, "hash", header.Hash())
			events = append(events, kernel.ChainEvent{Block: types.NewBlockWithHeader(header), Hash: header.Hash()})

		case kernel.SideStatTy:
			log4j.Debug("Inserted forked header", "number", header.Number, "hash", header.Hash())
			events = append(events, kernel.ChainSideEvent{Block: types.NewBlockWithHeader(header)})
		}
		return err
	}
	i, err := self.hc.InsertHeaderChain(chain, whFunc, start)
	self.postChainEvents(events)
	return i, err
}

func (self *LightChain) CurrentHeader() *types.Header {
	return self.hc.CurrentHeader()
}

func (self *LightChain) GetTd(hash common.Hash, number uint64) *big.Int {
	return self.hc.GetTd(hash, number)
}

func (self *LightChain) GetTdByHash(hash common.Hash) *big.Int {
	return self.hc.GetTdByHash(hash)
}

func (self *LightChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	return self.hc.GetHeader(hash, number)
}

func (self *LightChain) GetHeaderByHash(hash common.Hash) *types.Header {
	return self.hc.GetHeaderByHash(hash)
}

func (bc *LightChain) HasHeader(hash common.Hash, number uint64) bool {
	return bc.hc.HasHeader(hash, number)
}

func (self *LightChain) GetBlockHashesFromHash(hash common.Hash, max uint64) []common.Hash {
	return self.hc.GetBlockHashesFromHash(hash, max)
}

func (bc *LightChain) GetAncestor(hash common.Hash, number, ancestor uint64, maxNonCanonical *uint64) (common.Hash, uint64) {
	bc.chainmu.Lock()
	defer bc.chainmu.Unlock()

	return bc.hc.GetAncestor(hash, number, ancestor, maxNonCanonical)
}

func (self *LightChain) GetHeaderByNumber(number uint64) *types.Header {
	return self.hc.GetHeaderByNumber(number)
}

func (self *LightChain) GetHeaderByNumberOdr(ctx context.Context, number uint64) (*types.Header, error) {
	if header := self.hc.GetHeaderByNumber(number); header != nil {
		return header, nil
	}
	return GetHeaderByNumber(ctx, self.odr, number)
}

func (self *LightChain) Config() *params.ChainConfig { return self.hc.Config() }

func (self *LightChain) SyncCht(ctx context.Context) bool {
	if self.odr.ChtIndexer() == nil {
		return false
	}
	head := self.CurrentHeader().Number.Uint64()
	sections, _, _ := self.odr.ChtIndexer().Sections()

	latest := sections*CHTFrequencyClient - 1
	if clique := self.hc.Config().Clique; clique != nil {
		latest -= latest % clique.Epoch
	}
	if head >= latest {
		return false
	}
	if header, err := GetHeaderByNumber(ctx, self.odr, latest); header != nil && err == nil {
		self.mu.Lock()
		defer self.mu.Unlock()

		if self.hc.CurrentHeader().Number.Uint64() < header.Number.Uint64() {
			log4j.Info("Updated latest header based on CHT", "number", header.Number, "hash", header.Hash())
			self.hc.SetCurrentHeader(header)
		}
		return true
	}
	return false
}

func (self *LightChain) LockChain() {
	self.chainmu.RLock()
}

func (self *LightChain) UnlockChain() {
	self.chainmu.RUnlock()
}

func (self *LightChain) SubscribeChainEvent(ch chan<- kernel.ChainEvent) event.Subscription {
	return self.scope.Track(self.chainFeed.Subscribe(ch))
}

func (self *LightChain) SubscribeChainHeadEvent(ch chan<- kernel.ChainHeadEvent) event.Subscription {
	return self.scope.Track(self.chainHeadFeed.Subscribe(ch))
}

func (self *LightChain) SubscribeChainSideEvent(ch chan<- kernel.ChainSideEvent) event.Subscription {
	return self.scope.Track(self.chainSideFeed.Subscribe(ch))
}

func (self *LightChain) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return self.scope.Track(new(event.Feed).Subscribe(ch))
}

func (self *LightChain) SubscribeRemovedLogsEvent(ch chan<- kernel.RemovedLogsEvent) event.Subscription {
	return self.scope.Track(new(event.Feed).Subscribe(ch))
}
