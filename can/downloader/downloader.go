package downloader

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/metrics"
	"github.com/5uwifi/canchain/params"
)

var (
	MaxHashFetch    = 512
	MaxBlockFetch   = 128
	MaxHeaderFetch  = 192
	MaxSkeletonSize = 128
	MaxBodyFetch    = 128
	MaxReceiptFetch = 256
	MaxStateFetch   = 384

	MaxForkAncestry  = 3 * params.EpochDuration
	rttMinEstimate   = 2 * time.Second
	rttMaxEstimate   = 20 * time.Second
	rttMinConfidence = 0.1
	ttlScaling       = 3
	ttlLimit         = time.Minute

	qosTuningPeers   = 5
	qosConfidenceCap = 10
	qosTuningImpact  = 0.25

	maxQueuedHeaders  = 32 * 1024
	maxHeadersProcess = 2048
	maxResultsProcess = 2048

	reorgProtThreshold   = 48
	reorgProtHeaderDelay = 2

	fsHeaderCheckFrequency = 100
	fsHeaderSafetyNet      = 2048
	fsHeaderForceVerify    = 24
	fsHeaderContCheck      = 3 * time.Second
	fsMinFullBlocks        = 64
)

var (
	errBusy                    = errors.New("busy")
	errUnknownPeer             = errors.New("peer is unknown or unhealthy")
	errBadPeer                 = errors.New("action from bad peer ignored")
	errStallingPeer            = errors.New("peer is stalling")
	errNoPeers                 = errors.New("no peers to keep download active")
	errTimeout                 = errors.New("timeout")
	errEmptyHeaderSet          = errors.New("empty header set by peer")
	errPeersUnavailable        = errors.New("no peers available or all tried for download")
	errInvalidAncestor         = errors.New("retrieved ancestor is invalid")
	errInvalidChain            = errors.New("retrieved hash chain is invalid")
	errInvalidBlock            = errors.New("retrieved block is invalid")
	errInvalidBody             = errors.New("retrieved block body is invalid")
	errInvalidReceipt          = errors.New("retrieved receipt is invalid")
	errCancelBlockFetch        = errors.New("block download canceled (requested)")
	errCancelHeaderFetch       = errors.New("block header download canceled (requested)")
	errCancelBodyFetch         = errors.New("block body download canceled (requested)")
	errCancelReceiptFetch      = errors.New("receipt download canceled (requested)")
	errCancelStateFetch        = errors.New("state data download canceled (requested)")
	errCancelHeaderProcessing  = errors.New("header processing canceled (requested)")
	errCancelContentProcessing = errors.New("content processing canceled (requested)")
	errNoSyncActive            = errors.New("no sync active")
	errTooOld                  = errors.New("peer doesn't speak recent enough protocol version (need version >= 62)")
)

type Downloader struct {
	mode SyncMode
	mux  *event.TypeMux

	queue   *queue
	peers   *peerSet
	stateDB candb.Database

	rttEstimate   uint64
	rttConfidence uint64

	syncStatsChainOrigin uint64
	syncStatsChainHeight uint64
	syncStatsState       stateSyncStats
	syncStatsLock        sync.RWMutex

	lightchain LightChain
	blockchain BlockChain

	dropPeer peerDropFn

	synchroniseMock func(id string, hash common.Hash) error
	synchronising   int32
	notified        int32
	committed       int32

	headerCh      chan dataPack
	bodyCh        chan dataPack
	receiptCh     chan dataPack
	bodyWakeCh    chan bool
	receiptWakeCh chan bool
	headerProcCh  chan []*types.Header

	stateSyncStart chan *stateSync
	trackStateReq  chan *stateReq
	stateCh        chan dataPack

	cancelPeer string
	cancelCh   chan struct{}
	cancelLock sync.RWMutex
	cancelWg   sync.WaitGroup

	quitCh   chan struct{}
	quitLock sync.RWMutex

	syncInitHook     func(uint64, uint64)
	bodyFetchHook    func([]*types.Header)
	receiptFetchHook func([]*types.Header)
	chainInsertHook  func([]*fetchResult)
}

type LightChain interface {
	HasHeader(common.Hash, uint64) bool

	GetHeaderByHash(common.Hash) *types.Header

	CurrentHeader() *types.Header

	GetTd(common.Hash, uint64) *big.Int

	InsertHeaderChain([]*types.Header, int) (int, error)

	Rollback([]common.Hash)
}

type BlockChain interface {
	LightChain

	HasBlock(common.Hash, uint64) bool

	GetBlockByHash(common.Hash) *types.Block

	CurrentBlock() *types.Block

	CurrentFastBlock() *types.Block

	FastSyncCommitHead(common.Hash) error

	InsertChain(types.Blocks) (int, error)

	InsertReceiptChain(types.Blocks, []types.Receipts) (int, error)
}

func New(mode SyncMode, stateDb candb.Database, mux *event.TypeMux, chain BlockChain, lightchain LightChain, dropPeer peerDropFn) *Downloader {
	if lightchain == nil {
		lightchain = chain
	}

	dl := &Downloader{
		mode:           mode,
		stateDB:        stateDb,
		mux:            mux,
		queue:          newQueue(),
		peers:          newPeerSet(),
		rttEstimate:    uint64(rttMaxEstimate),
		rttConfidence:  uint64(1000000),
		blockchain:     chain,
		lightchain:     lightchain,
		dropPeer:       dropPeer,
		headerCh:       make(chan dataPack, 1),
		bodyCh:         make(chan dataPack, 1),
		receiptCh:      make(chan dataPack, 1),
		bodyWakeCh:     make(chan bool, 1),
		receiptWakeCh:  make(chan bool, 1),
		headerProcCh:   make(chan []*types.Header, 1),
		quitCh:         make(chan struct{}),
		stateCh:        make(chan dataPack),
		stateSyncStart: make(chan *stateSync),
		syncStatsState: stateSyncStats{
			processed: rawdb.ReadFastTrieProgress(stateDb),
		},
		trackStateReq: make(chan *stateReq),
	}
	go dl.qosTuner()
	go dl.stateFetcher()
	return dl
}

func (d *Downloader) Progress() canchain.SyncProgress {
	d.syncStatsLock.RLock()
	defer d.syncStatsLock.RUnlock()

	current := uint64(0)
	switch d.mode {
	case FullSync:
		current = d.blockchain.CurrentBlock().NumberU64()
	case FastSync:
		current = d.blockchain.CurrentFastBlock().NumberU64()
	case LightSync:
		current = d.lightchain.CurrentHeader().Number.Uint64()
	}
	return canchain.SyncProgress{
		StartingBlock: d.syncStatsChainOrigin,
		CurrentBlock:  current,
		HighestBlock:  d.syncStatsChainHeight,
		PulledStates:  d.syncStatsState.processed,
		KnownStates:   d.syncStatsState.processed + d.syncStatsState.pending,
	}
}

func (d *Downloader) Synchronising() bool {
	return atomic.LoadInt32(&d.synchronising) > 0
}

func (d *Downloader) RegisterPeer(id string, version int, peer Peer) error {
	logger := log4j.New("peer", id)
	logger.Trace("Registering sync peer")
	if err := d.peers.Register(newPeerConnection(id, version, peer, logger)); err != nil {
		logger.Error("Failed to register sync peer", "err", err)
		return err
	}
	d.qosReduceConfidence()

	return nil
}

func (d *Downloader) RegisterLightPeer(id string, version int, peer LightPeer) error {
	return d.RegisterPeer(id, version, &lightPeerWrapper{peer})
}

func (d *Downloader) UnregisterPeer(id string) error {
	logger := log4j.New("peer", id)
	logger.Trace("Unregistering sync peer")
	if err := d.peers.Unregister(id); err != nil {
		logger.Error("Failed to unregister sync peer", "err", err)
		return err
	}
	d.queue.Revoke(id)

	d.cancelLock.RLock()
	master := id == d.cancelPeer
	d.cancelLock.RUnlock()

	if master {
		d.cancel()
	}
	return nil
}

func (d *Downloader) Synchronise(id string, head common.Hash, td *big.Int, mode SyncMode) error {
	err := d.synchronise(id, head, td, mode)
	switch err {
	case nil:
	case errBusy:

	case errTimeout, errBadPeer, errStallingPeer,
		errEmptyHeaderSet, errPeersUnavailable, errTooOld,
		errInvalidAncestor, errInvalidChain:
		log4j.Warn("Synchronisation failed, dropping peer", "peer", id, "err", err)
		if d.dropPeer == nil {
			log4j.Warn("Downloader wants to drop peer, but peerdrop-function is not set", "peer", id)
		} else {
			d.dropPeer(id)
		}
	default:
		log4j.Warn("Synchronisation failed, retrying", "err", err)
	}
	return err
}

func (d *Downloader) synchronise(id string, hash common.Hash, td *big.Int, mode SyncMode) error {
	if d.synchroniseMock != nil {
		return d.synchroniseMock(id, hash)
	}
	if !atomic.CompareAndSwapInt32(&d.synchronising, 0, 1) {
		return errBusy
	}
	defer atomic.StoreInt32(&d.synchronising, 0)

	if atomic.CompareAndSwapInt32(&d.notified, 0, 1) {
		log4j.Info("Block synchronisation started")
	}
	d.queue.Reset()
	d.peers.Reset()

	for _, ch := range []chan bool{d.bodyWakeCh, d.receiptWakeCh} {
		select {
		case <-ch:
		default:
		}
	}
	for _, ch := range []chan dataPack{d.headerCh, d.bodyCh, d.receiptCh} {
		for empty := false; !empty; {
			select {
			case <-ch:
			default:
				empty = true
			}
		}
	}
	for empty := false; !empty; {
		select {
		case <-d.headerProcCh:
		default:
			empty = true
		}
	}
	d.cancelLock.Lock()
	d.cancelCh = make(chan struct{})
	d.cancelPeer = id
	d.cancelLock.Unlock()

	defer d.Cancel()

	d.mode = mode

	p := d.peers.Peer(id)
	if p == nil {
		return errUnknownPeer
	}
	return d.syncWithPeer(p, hash, td)
}

func (d *Downloader) syncWithPeer(p *peerConnection, hash common.Hash, td *big.Int) (err error) {
	d.mux.Post(StartEvent{})
	defer func() {
		if err != nil {
			d.mux.Post(FailedEvent{err})
		} else {
			d.mux.Post(DoneEvent{})
		}
	}()
	if p.version < 62 {
		return errTooOld
	}

	log4j.Debug("Synchronising with the network", "peer", p.id, "can", p.version, "head", hash, "td", td, "mode", d.mode)
	defer func(start time.Time) {
		log4j.Debug("Synchronisation terminated", "elapsed", time.Since(start))
	}(time.Now())

	latest, err := d.fetchHeight(p)
	if err != nil {
		return err
	}
	height := latest.Number.Uint64()

	origin, err := d.findAncestor(p, height)
	if err != nil {
		return err
	}
	d.syncStatsLock.Lock()
	if d.syncStatsChainHeight <= origin || d.syncStatsChainOrigin > origin {
		d.syncStatsChainOrigin = origin
	}
	d.syncStatsChainHeight = height
	d.syncStatsLock.Unlock()

	pivot := uint64(0)
	if d.mode == FastSync {
		if height <= uint64(fsMinFullBlocks) {
			origin = 0
		} else {
			pivot = height - uint64(fsMinFullBlocks)
			if pivot <= origin {
				origin = pivot - 1
			}
		}
	}
	d.committed = 1
	if d.mode == FastSync && pivot != 0 {
		d.committed = 0
	}
	d.queue.Prepare(origin+1, d.mode)
	if d.syncInitHook != nil {
		d.syncInitHook(origin, height)
	}

	fetchers := []func() error{
		func() error { return d.fetchHeaders(p, origin+1, pivot) },
		func() error { return d.fetchBodies(origin + 1) },
		func() error { return d.fetchReceipts(origin + 1) },
		func() error { return d.processHeaders(origin+1, pivot, td) },
	}
	if d.mode == FastSync {
		fetchers = append(fetchers, func() error { return d.processFastSyncContent(latest) })
	} else if d.mode == FullSync {
		fetchers = append(fetchers, d.processFullSyncContent)
	}
	return d.spawnSync(fetchers)
}

func (d *Downloader) spawnSync(fetchers []func() error) error {
	errc := make(chan error, len(fetchers))
	d.cancelWg.Add(len(fetchers))
	for _, fn := range fetchers {
		fn := fn
		go func() { defer d.cancelWg.Done(); errc <- fn() }()
	}
	var err error
	for i := 0; i < len(fetchers); i++ {
		if i == len(fetchers)-1 {
			d.queue.Close()
		}
		if err = <-errc; err != nil {
			break
		}
	}
	d.queue.Close()
	d.Cancel()
	return err
}

func (d *Downloader) cancel() {
	d.cancelLock.Lock()
	if d.cancelCh != nil {
		select {
		case <-d.cancelCh:
		default:
			close(d.cancelCh)
		}
	}
	d.cancelLock.Unlock()
}

func (d *Downloader) Cancel() {
	d.cancel()
	d.cancelWg.Wait()
}

func (d *Downloader) Terminate() {
	d.quitLock.Lock()
	select {
	case <-d.quitCh:
	default:
		close(d.quitCh)
	}
	d.quitLock.Unlock()

	d.Cancel()
}

func (d *Downloader) fetchHeight(p *peerConnection) (*types.Header, error) {
	p.log.Debug("Retrieving remote chain height")

	head, _ := p.peer.Head()
	go p.peer.RequestHeadersByHash(head, 1, 0, false)

	ttl := d.requestTTL()
	timeout := time.After(ttl)
	for {
		select {
		case <-d.cancelCh:
			return nil, errCancelBlockFetch

		case packet := <-d.headerCh:
			if packet.PeerId() != p.id {
				log4j.Debug("Received headers from incorrect peer", "peer", packet.PeerId())
				break
			}
			headers := packet.(*headerPack).headers
			if len(headers) != 1 {
				p.log.Debug("Multiple headers for single request", "headers", len(headers))
				return nil, errBadPeer
			}
			head := headers[0]
			p.log.Debug("Remote head header identified", "number", head.Number, "hash", head.Hash())
			return head, nil

		case <-timeout:
			p.log.Debug("Waiting for head header timed out", "elapsed", ttl)
			return nil, errTimeout

		case <-d.bodyCh:
		case <-d.receiptCh:
		}
	}
}

func (d *Downloader) findAncestor(p *peerConnection, height uint64) (uint64, error) {
	floor, ceil := int64(-1), d.lightchain.CurrentHeader().Number.Uint64()

	if d.mode == FullSync {
		ceil = d.blockchain.CurrentBlock().NumberU64()
	} else if d.mode == FastSync {
		ceil = d.blockchain.CurrentFastBlock().NumberU64()
	}
	if ceil >= MaxForkAncestry {
		floor = int64(ceil - MaxForkAncestry)
	}
	p.log.Debug("Looking for common ancestor", "local", ceil, "remote", height)

	head := ceil
	if head > height {
		head = height
	}
	from := int64(head) - int64(MaxHeaderFetch)
	if from < 0 {
		from = 0
	}
	limit := 2 * MaxHeaderFetch / 16
	count := 1 + int((int64(ceil)-from)/16)
	if count > limit {
		count = limit
	}
	go p.peer.RequestHeadersByNumber(uint64(from), count, 15, false)

	number, hash := uint64(0), common.Hash{}

	ttl := d.requestTTL()
	timeout := time.After(ttl)

	for finished := false; !finished; {
		select {
		case <-d.cancelCh:
			return 0, errCancelHeaderFetch

		case packet := <-d.headerCh:
			if packet.PeerId() != p.id {
				log4j.Debug("Received headers from incorrect peer", "peer", packet.PeerId())
				break
			}
			headers := packet.(*headerPack).headers
			if len(headers) == 0 {
				p.log.Warn("Empty head header set")
				return 0, errEmptyHeaderSet
			}
			for i := 0; i < len(headers); i++ {
				if number := headers[i].Number.Int64(); number != from+int64(i)*16 {
					p.log.Warn("Head headers broke chain ordering", "index", i, "requested", from+int64(i)*16, "received", number)
					return 0, errInvalidChain
				}
			}
			finished = true
			for i := len(headers) - 1; i >= 0; i-- {
				if headers[i].Number.Int64() < from || headers[i].Number.Uint64() > ceil {
					continue
				}
				h := headers[i].Hash()
				n := headers[i].Number.Uint64()
				if (d.mode == FullSync && d.blockchain.HasBlock(h, n)) || (d.mode != FullSync && d.lightchain.HasHeader(h, n)) {
					number, hash = n, h

					if number > height && i == limit-1 {
						p.log.Warn("Lied about chain head", "reported", height, "found", number)
						return 0, errStallingPeer
					}
					break
				}
			}

		case <-timeout:
			p.log.Debug("Waiting for head header timed out", "elapsed", ttl)
			return 0, errTimeout

		case <-d.bodyCh:
		case <-d.receiptCh:
		}
	}
	if hash != (common.Hash{}) {
		if int64(number) <= floor {
			p.log.Warn("Ancestor below allowance", "number", number, "hash", hash, "allowance", floor)
			return 0, errInvalidAncestor
		}
		p.log.Debug("Found common ancestor", "number", number, "hash", hash)
		return number, nil
	}
	start, end := uint64(0), head
	if floor > 0 {
		start = uint64(floor)
	}
	for start+1 < end {
		check := (start + end) / 2

		ttl := d.requestTTL()
		timeout := time.After(ttl)

		go p.peer.RequestHeadersByNumber(check, 1, 0, false)

		for arrived := false; !arrived; {
			select {
			case <-d.cancelCh:
				return 0, errCancelHeaderFetch

			case packer := <-d.headerCh:
				if packer.PeerId() != p.id {
					log4j.Debug("Received headers from incorrect peer", "peer", packer.PeerId())
					break
				}
				headers := packer.(*headerPack).headers
				if len(headers) != 1 {
					p.log.Debug("Multiple headers for single request", "headers", len(headers))
					return 0, errBadPeer
				}
				arrived = true

				h := headers[0].Hash()
				n := headers[0].Number.Uint64()
				if (d.mode == FullSync && !d.blockchain.HasBlock(h, n)) || (d.mode != FullSync && !d.lightchain.HasHeader(h, n)) {
					end = check
					break
				}
				header := d.lightchain.GetHeaderByHash(h)
				if header.Number.Uint64() != check {
					p.log.Debug("Received non requested header", "number", header.Number, "hash", header.Hash(), "request", check)
					return 0, errBadPeer
				}
				start = check

			case <-timeout:
				p.log.Debug("Waiting for search header timed out", "elapsed", ttl)
				return 0, errTimeout

			case <-d.bodyCh:
			case <-d.receiptCh:
			}
		}
	}
	if int64(start) <= floor {
		p.log.Warn("Ancestor below allowance", "number", start, "hash", hash, "allowance", floor)
		return 0, errInvalidAncestor
	}
	p.log.Debug("Found common ancestor", "number", start, "hash", hash)
	return start, nil
}

func (d *Downloader) fetchHeaders(p *peerConnection, from uint64, pivot uint64) error {
	p.log.Debug("Directing header downloads", "origin", from)
	defer p.log.Debug("Header download terminated")

	skeleton := true
	request := time.Now()
	timeout := time.NewTimer(0)
	<-timeout.C
	defer timeout.Stop()

	var ttl time.Duration
	getHeaders := func(from uint64) {
		request = time.Now()

		ttl = d.requestTTL()
		timeout.Reset(ttl)

		if skeleton {
			p.log.Trace("Fetching skeleton headers", "count", MaxHeaderFetch, "from", from)
			go p.peer.RequestHeadersByNumber(from+uint64(MaxHeaderFetch)-1, MaxSkeletonSize, MaxHeaderFetch-1, false)
		} else {
			p.log.Trace("Fetching full headers", "count", MaxHeaderFetch, "from", from)
			go p.peer.RequestHeadersByNumber(from, MaxHeaderFetch, 0, false)
		}
	}
	getHeaders(from)

	for {
		select {
		case <-d.cancelCh:
			return errCancelHeaderFetch

		case packet := <-d.headerCh:
			if packet.PeerId() != p.id {
				log4j.Debug("Received skeleton from incorrect peer", "peer", packet.PeerId())
				break
			}
			headerReqTimer.UpdateSince(request)
			timeout.Stop()

			if packet.Items() == 0 && skeleton {
				skeleton = false
				getHeaders(from)
				continue
			}
			if packet.Items() == 0 {
				if atomic.LoadInt32(&d.committed) == 0 && pivot <= from {
					p.log.Debug("No headers, waiting for pivot commit")
					select {
					case <-time.After(fsHeaderContCheck):
						getHeaders(from)
						continue
					case <-d.cancelCh:
						return errCancelHeaderFetch
					}
				}
				p.log.Debug("No more headers available")
				select {
				case d.headerProcCh <- nil:
					return nil
				case <-d.cancelCh:
					return errCancelHeaderFetch
				}
			}
			headers := packet.(*headerPack).headers

			if skeleton {
				filled, proced, err := d.fillHeaderSkeleton(from, headers)
				if err != nil {
					p.log.Debug("Skeleton chain invalid", "err", err)
					return errInvalidChain
				}
				headers = filled[proced:]
				from += uint64(proced)
			} else {
				if n := len(headers); n > 0 {
					head := uint64(0)
					if d.mode == LightSync {
						head = d.lightchain.CurrentHeader().Number.Uint64()
					} else {
						head = d.blockchain.CurrentFastBlock().NumberU64()
						if full := d.blockchain.CurrentBlock().NumberU64(); head < full {
							head = full
						}
					}
					if head+uint64(reorgProtThreshold) < headers[n-1].Number.Uint64() {
						delay := reorgProtHeaderDelay
						if delay > n {
							delay = n
						}
						headers = headers[:n-delay]
					}
				}
			}
			if len(headers) > 0 {
				p.log.Trace("Scheduling new headers", "count", len(headers), "from", from)
				select {
				case d.headerProcCh <- headers:
				case <-d.cancelCh:
					return errCancelHeaderFetch
				}
				from += uint64(len(headers))
				getHeaders(from)
			} else {
				p.log.Trace("All headers delayed, waiting")
				select {
				case <-time.After(fsHeaderContCheck):
					getHeaders(from)
					continue
				case <-d.cancelCh:
					return errCancelHeaderFetch
				}
			}

		case <-timeout.C:
			if d.dropPeer == nil {
				p.log.Warn("Downloader wants to drop peer, but peerdrop-function is not set", "peer", p.id)
				break
			}
			p.log.Debug("Header request timed out", "elapsed", ttl)
			headerTimeoutMeter.Mark(1)
			d.dropPeer(p.id)

			for _, ch := range []chan bool{d.bodyWakeCh, d.receiptWakeCh} {
				select {
				case ch <- false:
				case <-d.cancelCh:
				}
			}
			select {
			case d.headerProcCh <- nil:
			case <-d.cancelCh:
			}
			return errBadPeer
		}
	}
}

func (d *Downloader) fillHeaderSkeleton(from uint64, skeleton []*types.Header) ([]*types.Header, int, error) {
	log4j.Debug("Filling up skeleton", "from", from)
	d.queue.ScheduleSkeleton(from, skeleton)

	var (
		deliver = func(packet dataPack) (int, error) {
			pack := packet.(*headerPack)
			return d.queue.DeliverHeaders(pack.peerID, pack.headers, d.headerProcCh)
		}
		expire   = func() map[string]int { return d.queue.ExpireHeaders(d.requestTTL()) }
		throttle = func() bool { return false }
		reserve  = func(p *peerConnection, count int) (*fetchRequest, bool, error) {
			return d.queue.ReserveHeaders(p, count), false, nil
		}
		fetch    = func(p *peerConnection, req *fetchRequest) error { return p.FetchHeaders(req.From, MaxHeaderFetch) }
		capacity = func(p *peerConnection) int { return p.HeaderCapacity(d.requestRTT()) }
		setIdle  = func(p *peerConnection, accepted int) { p.SetHeadersIdle(accepted) }
	)
	err := d.fetchParts(errCancelHeaderFetch, d.headerCh, deliver, d.queue.headerContCh, expire,
		d.queue.PendingHeaders, d.queue.InFlightHeaders, throttle, reserve,
		nil, fetch, d.queue.CancelHeaders, capacity, d.peers.HeaderIdlePeers, setIdle, "headers")

	log4j.Debug("Skeleton fill terminated", "err", err)

	filled, proced := d.queue.RetrieveHeaders()
	return filled, proced, err
}

func (d *Downloader) fetchBodies(from uint64) error {
	log4j.Debug("Downloading block bodies", "origin", from)

	var (
		deliver = func(packet dataPack) (int, error) {
			pack := packet.(*bodyPack)
			return d.queue.DeliverBodies(pack.peerID, pack.transactions, pack.uncles)
		}
		expire   = func() map[string]int { return d.queue.ExpireBodies(d.requestTTL()) }
		fetch    = func(p *peerConnection, req *fetchRequest) error { return p.FetchBodies(req) }
		capacity = func(p *peerConnection) int { return p.BlockCapacity(d.requestRTT()) }
		setIdle  = func(p *peerConnection, accepted int) { p.SetBodiesIdle(accepted) }
	)
	err := d.fetchParts(errCancelBodyFetch, d.bodyCh, deliver, d.bodyWakeCh, expire,
		d.queue.PendingBlocks, d.queue.InFlightBlocks, d.queue.ShouldThrottleBlocks, d.queue.ReserveBodies,
		d.bodyFetchHook, fetch, d.queue.CancelBodies, capacity, d.peers.BodyIdlePeers, setIdle, "bodies")

	log4j.Debug("Block body download terminated", "err", err)
	return err
}

func (d *Downloader) fetchReceipts(from uint64) error {
	log4j.Debug("Downloading transaction receipts", "origin", from)

	var (
		deliver = func(packet dataPack) (int, error) {
			pack := packet.(*receiptPack)
			return d.queue.DeliverReceipts(pack.peerID, pack.receipts)
		}
		expire   = func() map[string]int { return d.queue.ExpireReceipts(d.requestTTL()) }
		fetch    = func(p *peerConnection, req *fetchRequest) error { return p.FetchReceipts(req) }
		capacity = func(p *peerConnection) int { return p.ReceiptCapacity(d.requestRTT()) }
		setIdle  = func(p *peerConnection, accepted int) { p.SetReceiptsIdle(accepted) }
	)
	err := d.fetchParts(errCancelReceiptFetch, d.receiptCh, deliver, d.receiptWakeCh, expire,
		d.queue.PendingReceipts, d.queue.InFlightReceipts, d.queue.ShouldThrottleReceipts, d.queue.ReserveReceipts,
		d.receiptFetchHook, fetch, d.queue.CancelReceipts, capacity, d.peers.ReceiptIdlePeers, setIdle, "receipts")

	log4j.Debug("Transaction receipt download terminated", "err", err)
	return err
}

func (d *Downloader) fetchParts(errCancel error, deliveryCh chan dataPack, deliver func(dataPack) (int, error), wakeCh chan bool,
	expire func() map[string]int, pending func() int, inFlight func() bool, throttle func() bool, reserve func(*peerConnection, int) (*fetchRequest, bool, error),
	fetchHook func([]*types.Header), fetch func(*peerConnection, *fetchRequest) error, cancel func(*fetchRequest), capacity func(*peerConnection) int,
	idle func() ([]*peerConnection, int), setIdle func(*peerConnection, int), kind string) error {

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	update := make(chan struct{}, 1)

	finished := false
	for {
		select {
		case <-d.cancelCh:
			return errCancel

		case packet := <-deliveryCh:
			if peer := d.peers.Peer(packet.PeerId()); peer != nil {
				accepted, err := deliver(packet)
				if err == errInvalidChain {
					return err
				}
				if err != errStaleDelivery {
					setIdle(peer, accepted)
				}
				switch {
				case err == nil && packet.Items() == 0:
					peer.log.Trace("Requested data not delivered", "type", kind)
				case err == nil:
					peer.log.Trace("Delivered new batch of data", "type", kind, "count", packet.Stats())
				default:
					peer.log.Trace("Failed to deliver retrieved data", "type", kind, "err", err)
				}
			}
			select {
			case update <- struct{}{}:
			default:
			}

		case cont := <-wakeCh:
			if !cont {
				finished = true
			}
			select {
			case update <- struct{}{}:
			default:
			}

		case <-ticker.C:
			select {
			case update <- struct{}{}:
			default:
			}

		case <-update:
			if d.peers.Len() == 0 {
				return errNoPeers
			}
			for pid, fails := range expire() {
				if peer := d.peers.Peer(pid); peer != nil {
					if fails > 2 {
						peer.log.Trace("Data delivery timed out", "type", kind)
						setIdle(peer, 0)
					} else {
						peer.log.Debug("Stalling delivery, dropping", "type", kind)
						if d.dropPeer == nil {
							peer.log.Warn("Downloader wants to drop peer, but peerdrop-function is not set", "peer", pid)
						} else {
							d.dropPeer(pid)
						}
					}
				}
			}
			if pending() == 0 {
				if !inFlight() && finished {
					log4j.Debug("Data fetching completed", "type", kind)
					return nil
				}
				break
			}
			progressed, throttled, running := false, false, inFlight()
			idles, total := idle()

			for _, peer := range idles {
				if throttle() {
					throttled = true
					break
				}
				if pending() == 0 {
					break
				}
				request, progress, err := reserve(peer, capacity(peer))
				if err != nil {
					return err
				}
				if progress {
					progressed = true
				}
				if request == nil {
					continue
				}
				if request.From > 0 {
					peer.log.Trace("Requesting new batch of data", "type", kind, "from", request.From)
				} else {
					peer.log.Trace("Requesting new batch of data", "type", kind, "count", len(request.Headers), "from", request.Headers[0].Number)
				}
				if fetchHook != nil {
					fetchHook(request.Headers)
				}
				if err := fetch(peer, request); err != nil {
					panic(fmt.Sprintf("%v: %s fetch assignment failed", peer, kind))
				}
				running = true
			}
			if !progressed && !throttled && !running && len(idles) == total && pending() > 0 {
				return errPeersUnavailable
			}
		}
	}
}

func (d *Downloader) processHeaders(origin uint64, pivot uint64, td *big.Int) error {
	rollback := []*types.Header{}
	defer func() {
		if len(rollback) > 0 {
			hashes := make([]common.Hash, len(rollback))
			for i, header := range rollback {
				hashes[i] = header.Hash()
			}
			lastHeader, lastFastBlock, lastBlock := d.lightchain.CurrentHeader().Number, common.Big0, common.Big0
			if d.mode != LightSync {
				lastFastBlock = d.blockchain.CurrentFastBlock().Number()
				lastBlock = d.blockchain.CurrentBlock().Number()
			}
			d.lightchain.Rollback(hashes)
			curFastBlock, curBlock := common.Big0, common.Big0
			if d.mode != LightSync {
				curFastBlock = d.blockchain.CurrentFastBlock().Number()
				curBlock = d.blockchain.CurrentBlock().Number()
			}
			log4j.Warn("Rolled back headers", "count", len(hashes),
				"header", fmt.Sprintf("%d->%d", lastHeader, d.lightchain.CurrentHeader().Number),
				"fast", fmt.Sprintf("%d->%d", lastFastBlock, curFastBlock),
				"block", fmt.Sprintf("%d->%d", lastBlock, curBlock))
		}
	}()

	gotHeaders := false

	for {
		select {
		case <-d.cancelCh:
			return errCancelHeaderProcessing

		case headers := <-d.headerProcCh:
			if len(headers) == 0 {
				for _, ch := range []chan bool{d.bodyWakeCh, d.receiptWakeCh} {
					select {
					case ch <- false:
					case <-d.cancelCh:
					}
				}
				if d.mode != LightSync {
					head := d.blockchain.CurrentBlock()
					if !gotHeaders && td.Cmp(d.blockchain.GetTd(head.Hash(), head.NumberU64())) > 0 {
						return errStallingPeer
					}
				}
				if d.mode == FastSync || d.mode == LightSync {
					head := d.lightchain.CurrentHeader()
					if td.Cmp(d.lightchain.GetTd(head.Hash(), head.Number.Uint64())) > 0 {
						return errStallingPeer
					}
				}
				rollback = nil
				return nil
			}
			gotHeaders = true

			for len(headers) > 0 {
				select {
				case <-d.cancelCh:
					return errCancelHeaderProcessing
				default:
				}
				limit := maxHeadersProcess
				if limit > len(headers) {
					limit = len(headers)
				}
				chunk := headers[:limit]

				if d.mode == FastSync || d.mode == LightSync {
					unknown := make([]*types.Header, 0, len(headers))
					for _, header := range chunk {
						if !d.lightchain.HasHeader(header.Hash(), header.Number.Uint64()) {
							unknown = append(unknown, header)
						}
					}
					frequency := fsHeaderCheckFrequency
					if chunk[len(chunk)-1].Number.Uint64()+uint64(fsHeaderForceVerify) > pivot {
						frequency = 1
					}
					if n, err := d.lightchain.InsertHeaderChain(chunk, frequency); err != nil {
						if n > 0 {
							rollback = append(rollback, chunk[:n]...)
						}
						log4j.Debug("Invalid header encountered", "number", chunk[n].Number, "hash", chunk[n].Hash(), "err", err)
						return errInvalidChain
					}
					rollback = append(rollback, unknown...)
					if len(rollback) > fsHeaderSafetyNet {
						rollback = append(rollback[:0], rollback[len(rollback)-fsHeaderSafetyNet:]...)
					}
				}
				if d.mode == FullSync || d.mode == FastSync {
					for d.queue.PendingBlocks() >= maxQueuedHeaders || d.queue.PendingReceipts() >= maxQueuedHeaders {
						select {
						case <-d.cancelCh:
							return errCancelHeaderProcessing
						case <-time.After(time.Second):
						}
					}
					inserts := d.queue.Schedule(chunk, origin)
					if len(inserts) != len(chunk) {
						log4j.Debug("Stale headers")
						return errBadPeer
					}
				}
				headers = headers[limit:]
				origin += uint64(limit)
			}

			d.syncStatsLock.Lock()
			if d.syncStatsChainHeight < origin {
				d.syncStatsChainHeight = origin - 1
			}
			d.syncStatsLock.Unlock()

			for _, ch := range []chan bool{d.bodyWakeCh, d.receiptWakeCh} {
				select {
				case ch <- true:
				default:
				}
			}
		}
	}
}

func (d *Downloader) processFullSyncContent() error {
	for {
		results := d.queue.Results(true)
		if len(results) == 0 {
			return nil
		}
		if d.chainInsertHook != nil {
			d.chainInsertHook(results)
		}
		if err := d.importBlockResults(results); err != nil {
			return err
		}
	}
}

func (d *Downloader) importBlockResults(results []*fetchResult) error {
	if len(results) == 0 {
		return nil
	}
	select {
	case <-d.quitCh:
		return errCancelContentProcessing
	default:
	}
	first, last := results[0].Header, results[len(results)-1].Header
	log4j.Debug("Inserting downloaded chain", "items", len(results),
		"firstnum", first.Number, "firsthash", first.Hash(),
		"lastnum", last.Number, "lasthash", last.Hash(),
	)
	blocks := make([]*types.Block, len(results))
	for i, result := range results {
		blocks[i] = types.NewBlockWithHeader(result.Header).WithBody(result.Transactions, result.Uncles)
	}
	if index, err := d.blockchain.InsertChain(blocks); err != nil {
		log4j.Debug("Downloaded item processing failed", "number", results[index].Header.Number, "hash", results[index].Header.Hash(), "err", err)
		return errInvalidChain
	}
	return nil
}

func (d *Downloader) processFastSyncContent(latest *types.Header) error {
	stateSync := d.syncState(latest.Root)
	defer stateSync.Cancel()
	go func() {
		if err := stateSync.Wait(); err != nil && err != errCancelStateFetch {
			d.queue.Close()
		}
	}()
	pivot := uint64(0)
	if height := latest.Number.Uint64(); height > uint64(fsMinFullBlocks) {
		pivot = height - uint64(fsMinFullBlocks)
	}
	var (
		oldPivot *fetchResult
		oldTail  []*fetchResult
	)
	for {
		results := d.queue.Results(oldPivot == nil)
		if len(results) == 0 {
			if oldPivot == nil {
				return stateSync.Cancel()
			}
			select {
			case <-d.cancelCh:
				return stateSync.Cancel()
			default:
			}
		}
		if d.chainInsertHook != nil {
			d.chainInsertHook(results)
		}
		if oldPivot != nil {
			results = append(append([]*fetchResult{oldPivot}, oldTail...), results...)
		}
		if atomic.LoadInt32(&d.committed) == 0 {
			latest = results[len(results)-1].Header
			if height := latest.Number.Uint64(); height > pivot+2*uint64(fsMinFullBlocks) {
				log4j.Warn("Pivot became stale, moving", "old", pivot, "new", height-uint64(fsMinFullBlocks))
				pivot = height - uint64(fsMinFullBlocks)
			}
		}
		P, beforeP, afterP := splitAroundPivot(pivot, results)
		if err := d.commitFastSyncData(beforeP, stateSync); err != nil {
			return err
		}
		if P != nil {
			if oldPivot != P {
				stateSync.Cancel()

				stateSync = d.syncState(P.Header.Root)
				defer stateSync.Cancel()
				go func() {
					if err := stateSync.Wait(); err != nil && err != errCancelStateFetch {
						d.queue.Close()
					}
				}()
				oldPivot = P
			}
			select {
			case <-stateSync.done:
				if stateSync.err != nil {
					return stateSync.err
				}
				if err := d.commitPivotBlock(P); err != nil {
					return err
				}
				oldPivot = nil

			case <-time.After(time.Second):
				oldTail = afterP
				continue
			}
		}
		if err := d.importBlockResults(afterP); err != nil {
			return err
		}
	}
}

func splitAroundPivot(pivot uint64, results []*fetchResult) (p *fetchResult, before, after []*fetchResult) {
	for _, result := range results {
		num := result.Header.Number.Uint64()
		switch {
		case num < pivot:
			before = append(before, result)
		case num == pivot:
			p = result
		default:
			after = append(after, result)
		}
	}
	return p, before, after
}

func (d *Downloader) commitFastSyncData(results []*fetchResult, stateSync *stateSync) error {
	if len(results) == 0 {
		return nil
	}
	select {
	case <-d.quitCh:
		return errCancelContentProcessing
	case <-stateSync.done:
		if err := stateSync.Wait(); err != nil {
			return err
		}
	default:
	}
	first, last := results[0].Header, results[len(results)-1].Header
	log4j.Debug("Inserting fast-sync blocks", "items", len(results),
		"firstnum", first.Number, "firsthash", first.Hash(),
		"lastnumn", last.Number, "lasthash", last.Hash(),
	)
	blocks := make([]*types.Block, len(results))
	receipts := make([]types.Receipts, len(results))
	for i, result := range results {
		blocks[i] = types.NewBlockWithHeader(result.Header).WithBody(result.Transactions, result.Uncles)
		receipts[i] = result.Receipts
	}
	if index, err := d.blockchain.InsertReceiptChain(blocks, receipts); err != nil {
		log4j.Debug("Downloaded item processing failed", "number", results[index].Header.Number, "hash", results[index].Header.Hash(), "err", err)
		return errInvalidChain
	}
	return nil
}

func (d *Downloader) commitPivotBlock(result *fetchResult) error {
	block := types.NewBlockWithHeader(result.Header).WithBody(result.Transactions, result.Uncles)
	log4j.Debug("Committing fast sync pivot as new head", "number", block.Number(), "hash", block.Hash())
	if _, err := d.blockchain.InsertReceiptChain([]*types.Block{block}, []types.Receipts{result.Receipts}); err != nil {
		return err
	}
	if err := d.blockchain.FastSyncCommitHead(block.Hash()); err != nil {
		return err
	}
	atomic.StoreInt32(&d.committed, 1)
	return nil
}

func (d *Downloader) DeliverHeaders(id string, headers []*types.Header) (err error) {
	return d.deliver(id, d.headerCh, &headerPack{id, headers}, headerInMeter, headerDropMeter)
}

func (d *Downloader) DeliverBodies(id string, transactions [][]*types.Transaction, uncles [][]*types.Header) (err error) {
	return d.deliver(id, d.bodyCh, &bodyPack{id, transactions, uncles}, bodyInMeter, bodyDropMeter)
}

func (d *Downloader) DeliverReceipts(id string, receipts [][]*types.Receipt) (err error) {
	return d.deliver(id, d.receiptCh, &receiptPack{id, receipts}, receiptInMeter, receiptDropMeter)
}

func (d *Downloader) DeliverNodeData(id string, data [][]byte) (err error) {
	return d.deliver(id, d.stateCh, &statePack{id, data}, stateInMeter, stateDropMeter)
}

func (d *Downloader) deliver(id string, destCh chan dataPack, packet dataPack, inMeter, dropMeter metrics.Meter) (err error) {
	inMeter.Mark(int64(packet.Items()))
	defer func() {
		if err != nil {
			dropMeter.Mark(int64(packet.Items()))
		}
	}()
	d.cancelLock.RLock()
	cancel := d.cancelCh
	d.cancelLock.RUnlock()
	if cancel == nil {
		return errNoSyncActive
	}
	select {
	case destCh <- packet:
		return nil
	case <-cancel:
		return errNoSyncActive
	}
}

func (d *Downloader) qosTuner() {
	for {
		rtt := time.Duration((1-qosTuningImpact)*float64(atomic.LoadUint64(&d.rttEstimate)) + qosTuningImpact*float64(d.peers.medianRTT()))
		atomic.StoreUint64(&d.rttEstimate, uint64(rtt))

		conf := atomic.LoadUint64(&d.rttConfidence)
		conf = conf + (1000000-conf)/2
		atomic.StoreUint64(&d.rttConfidence, conf)

		log4j.Debug("Recalculated downloader QoS values", "rtt", rtt, "confidence", float64(conf)/1000000.0, "ttl", d.requestTTL())
		select {
		case <-d.quitCh:
			return
		case <-time.After(rtt):
		}
	}
}

func (d *Downloader) qosReduceConfidence() {
	peers := uint64(d.peers.Len())
	if peers == 0 {
		return
	}
	if peers == 1 {
		atomic.StoreUint64(&d.rttConfidence, 1000000)
		return
	}
	if peers >= uint64(qosConfidenceCap) {
		return
	}
	conf := atomic.LoadUint64(&d.rttConfidence) * (peers - 1) / peers
	if float64(conf)/1000000 < rttMinConfidence {
		conf = uint64(rttMinConfidence * 1000000)
	}
	atomic.StoreUint64(&d.rttConfidence, conf)

	rtt := time.Duration(atomic.LoadUint64(&d.rttEstimate))
	log4j.Debug("Relaxed downloader QoS values", "rtt", rtt, "confidence", float64(conf)/1000000.0, "ttl", d.requestTTL())
}

func (d *Downloader) requestRTT() time.Duration {
	return time.Duration(atomic.LoadUint64(&d.rttEstimate)) * 9 / 10
}

func (d *Downloader) requestTTL() time.Duration {
	var (
		rtt  = time.Duration(atomic.LoadUint64(&d.rttEstimate))
		conf = float64(atomic.LoadUint64(&d.rttConfidence)) / 1000000.0
	)
	ttl := time.Duration(ttlScaling) * time.Duration(float64(rtt)/conf)
	if ttl > ttlLimit {
		ttl = ttlLimit
	}
	return ttl
}
