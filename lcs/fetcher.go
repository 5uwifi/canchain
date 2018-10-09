package lcs

import (
	"math/big"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/mclock"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/light"
)

const (
	blockDelayTimeout    = time.Second * 10
	maxNodeCount         = 20
	serverStateAvailable = 100
)

type lightFetcher struct {
	pm    *ProtocolManager
	odr   *LesOdr
	chain *light.LightChain

	lock            sync.Mutex
	maxConfirmedTd  *big.Int
	peers           map[*peer]*fetcherPeerInfo
	lastUpdateStats *updateStatsEntry
	syncing         bool
	syncDone        chan *peer

	reqMu      sync.RWMutex
	requested  map[uint64]fetchRequest
	deliverChn chan fetchResponse
	timeoutChn chan uint64
	requestChn chan bool
}

type fetcherPeerInfo struct {
	root, lastAnnounced *fetcherTreeNode
	nodeCnt             int
	confirmedTd         *big.Int
	bestConfirmed       *fetcherTreeNode
	nodeByHash          map[common.Hash]*fetcherTreeNode
	firstUpdateStats    *updateStatsEntry
}

type fetcherTreeNode struct {
	hash             common.Hash
	number           uint64
	td               *big.Int
	known, requested bool
	parent           *fetcherTreeNode
	children         []*fetcherTreeNode
}

type fetchRequest struct {
	hash    common.Hash
	amount  uint64
	peer    *peer
	sent    mclock.AbsTime
	timeout bool
}

type fetchResponse struct {
	reqID   uint64
	headers []*types.Header
	peer    *peer
}

func newLightFetcher(pm *ProtocolManager) *lightFetcher {
	f := &lightFetcher{
		pm:             pm,
		chain:          pm.blockchain.(*light.LightChain),
		odr:            pm.odr,
		peers:          make(map[*peer]*fetcherPeerInfo),
		deliverChn:     make(chan fetchResponse, 100),
		requested:      make(map[uint64]fetchRequest),
		timeoutChn:     make(chan uint64),
		requestChn:     make(chan bool, 100),
		syncDone:       make(chan *peer),
		maxConfirmedTd: big.NewInt(0),
	}
	pm.peers.notify(f)

	f.pm.wg.Add(1)
	go f.syncLoop()
	return f
}

func (f *lightFetcher) syncLoop() {
	requesting := false
	defer f.pm.wg.Done()
	for {
		select {
		case <-f.pm.quitSync:
			return
		case newAnnounce := <-f.requestChn:
			f.lock.Lock()
			s := requesting
			requesting = false
			var (
				rq    *distReq
				reqID uint64
			)
			if !f.syncing && !(newAnnounce && s) {
				rq, reqID = f.nextRequest()
			}
			syncing := f.syncing
			f.lock.Unlock()

			if rq != nil {
				requesting = true
				_, ok := <-f.pm.reqDist.queue(rq)
				if !ok {
					f.requestChn <- false
				}

				if !syncing {
					go func() {
						time.Sleep(softRequestTimeout)
						f.reqMu.Lock()
						req, ok := f.requested[reqID]
						if ok {
							req.timeout = true
							f.requested[reqID] = req
						}
						f.reqMu.Unlock()
						f.requestChn <- false
					}()
				}
			}
		case reqID := <-f.timeoutChn:
			f.reqMu.Lock()
			req, ok := f.requested[reqID]
			if ok {
				delete(f.requested, reqID)
			}
			f.reqMu.Unlock()
			if ok {
				f.pm.serverPool.adjustResponseTime(req.peer.poolEntry, time.Duration(mclock.Now()-req.sent), true)
				req.peer.Log().Debug("Fetching data timed out hard")
				go f.pm.removePeer(req.peer.id)
			}
		case resp := <-f.deliverChn:
			f.reqMu.Lock()
			req, ok := f.requested[resp.reqID]
			if ok && req.peer != resp.peer {
				ok = false
			}
			if ok {
				delete(f.requested, resp.reqID)
			}
			f.reqMu.Unlock()
			if ok {
				f.pm.serverPool.adjustResponseTime(req.peer.poolEntry, time.Duration(mclock.Now()-req.sent), req.timeout)
			}
			f.lock.Lock()
			if !ok || !(f.syncing || f.processResponse(req, resp)) {
				resp.peer.Log().Debug("Failed processing response")
				go f.pm.removePeer(resp.peer.id)
			}
			f.lock.Unlock()
		case p := <-f.syncDone:
			f.lock.Lock()
			p.Log().Debug("Done synchronising with peer")
			f.checkSyncedHeaders(p)
			f.syncing = false
			f.lock.Unlock()
		}
	}
}

func (f *lightFetcher) registerPeer(p *peer) {
	p.lock.Lock()
	p.hasBlock = func(hash common.Hash, number uint64, hasState bool) bool {
		return f.peerHasBlock(p, hash, number, hasState)
	}
	p.lock.Unlock()

	f.lock.Lock()
	defer f.lock.Unlock()

	f.peers[p] = &fetcherPeerInfo{nodeByHash: make(map[common.Hash]*fetcherTreeNode)}
}

func (f *lightFetcher) unregisterPeer(p *peer) {
	p.lock.Lock()
	p.hasBlock = nil
	p.lock.Unlock()

	f.lock.Lock()
	defer f.lock.Unlock()

	f.checkUpdateStats(p, nil)
	delete(f.peers, p)
}

func (f *lightFetcher) announce(p *peer, head *announceData) {
	f.lock.Lock()
	defer f.lock.Unlock()
	p.Log().Debug("Received new announcement", "number", head.Number, "hash", head.Hash, "reorg", head.ReorgDepth)

	fp := f.peers[p]
	if fp == nil {
		p.Log().Debug("Announcement from unknown peer")
		return
	}

	if fp.lastAnnounced != nil && head.Td.Cmp(fp.lastAnnounced.td) <= 0 {
		p.Log().Debug("Received non-monotonic td", "current", head.Td, "previous", fp.lastAnnounced.td)
		go f.pm.removePeer(p.id)
		return
	}

	n := fp.lastAnnounced
	for i := uint64(0); i < head.ReorgDepth; i++ {
		if n == nil {
			break
		}
		n = n.parent
	}
	if n != nil && (head.Number >= n.number+maxNodeCount || head.Number <= n.number) {
		n = nil
		fp.nodeCnt = 0
		fp.nodeByHash = make(map[common.Hash]*fetcherTreeNode)
	}
	if n != nil {
		locked := false
		for uint64(fp.nodeCnt)+head.Number-n.number > maxNodeCount && fp.root != nil {
			if !locked {
				f.chain.LockChain()
				defer f.chain.UnlockChain()
				locked = true
			}
			var newRoot *fetcherTreeNode
			for i, nn := range fp.root.children {
				if rawdb.ReadCanonicalHash(f.pm.chainDb, nn.number) == nn.hash {
					fp.root.children = append(fp.root.children[:i], fp.root.children[i+1:]...)
					nn.parent = nil
					newRoot = nn
					break
				}
			}
			fp.deleteNode(fp.root)
			if n == fp.root {
				n = newRoot
			}
			fp.root = newRoot
			if newRoot == nil || !f.checkKnownNode(p, newRoot) {
				fp.bestConfirmed = nil
				fp.confirmedTd = nil
			}

			if n == nil {
				break
			}
		}
		if n != nil {
			for n.number < head.Number {
				nn := &fetcherTreeNode{number: n.number + 1, parent: n}
				n.children = append(n.children, nn)
				n = nn
				fp.nodeCnt++
			}
			n.hash = head.Hash
			n.td = head.Td
			fp.nodeByHash[n.hash] = n
		}
	}
	if n == nil {
		if fp.root != nil {
			fp.deleteNode(fp.root)
		}
		n = &fetcherTreeNode{hash: head.Hash, number: head.Number, td: head.Td}
		fp.root = n
		fp.nodeCnt++
		fp.nodeByHash[n.hash] = n
		fp.bestConfirmed = nil
		fp.confirmedTd = nil
	}

	f.checkKnownNode(p, n)
	p.lock.Lock()
	p.headInfo = head
	fp.lastAnnounced = n
	p.lock.Unlock()
	f.checkUpdateStats(p, nil)
	f.requestChn <- true
}

func (f *lightFetcher) peerHasBlock(p *peer, hash common.Hash, number uint64, hasState bool) bool {
	f.lock.Lock()
	defer f.lock.Unlock()

	fp := f.peers[p]
	if fp == nil || fp.root == nil {
		return false
	}

	if hasState {
		if fp.lastAnnounced == nil || fp.lastAnnounced.number > number+serverStateAvailable {
			return false
		}
	}

	if f.syncing {
		return true
	}

	if number >= fp.root.number {
		return fp.nodeByHash[hash] != nil
	}
	f.chain.LockChain()
	defer f.chain.UnlockChain()
	return rawdb.ReadCanonicalHash(f.pm.chainDb, fp.root.number) == fp.root.hash && rawdb.ReadCanonicalHash(f.pm.chainDb, number) == hash
}

func (f *lightFetcher) requestAmount(p *peer, n *fetcherTreeNode) uint64 {
	amount := uint64(0)
	nn := n
	for nn != nil && !f.checkKnownNode(p, nn) {
		nn = nn.parent
		amount++
	}
	if nn == nil {
		amount = n.number
	}
	return amount
}

func (f *lightFetcher) requestedID(reqID uint64) bool {
	f.reqMu.RLock()
	_, ok := f.requested[reqID]
	f.reqMu.RUnlock()
	return ok
}

func (f *lightFetcher) nextRequest() (*distReq, uint64) {
	var (
		bestHash   common.Hash
		bestAmount uint64
	)
	bestTd := f.maxConfirmedTd
	bestSyncing := false

	for p, fp := range f.peers {
		for hash, n := range fp.nodeByHash {
			if !f.checkKnownNode(p, n) && !n.requested && (bestTd == nil || n.td.Cmp(bestTd) >= 0) {
				amount := f.requestAmount(p, n)
				if bestTd == nil || n.td.Cmp(bestTd) > 0 || amount < bestAmount {
					bestHash = hash
					bestAmount = amount
					bestTd = n.td
					bestSyncing = fp.bestConfirmed == nil || fp.root == nil || !f.checkKnownNode(p, fp.root)
				}
			}
		}
	}
	if bestTd == f.maxConfirmedTd {
		return nil, 0
	}

	f.syncing = bestSyncing

	var rq *distReq
	reqID := genReqID()
	if f.syncing {
		rq = &distReq{
			getCost: func(dp distPeer) uint64 {
				return 0
			},
			canSend: func(dp distPeer) bool {
				p := dp.(*peer)
				f.lock.Lock()
				defer f.lock.Unlock()

				fp := f.peers[p]
				return fp != nil && fp.nodeByHash[bestHash] != nil
			},
			request: func(dp distPeer) func() {
				go func() {
					p := dp.(*peer)
					p.Log().Debug("Synchronisation started")
					f.pm.synchronise(p)
					f.syncDone <- p
				}()
				return nil
			},
		}
	} else {
		rq = &distReq{
			getCost: func(dp distPeer) uint64 {
				p := dp.(*peer)
				return p.GetRequestCost(GetBlockHeadersMsg, int(bestAmount))
			},
			canSend: func(dp distPeer) bool {
				p := dp.(*peer)
				f.lock.Lock()
				defer f.lock.Unlock()

				fp := f.peers[p]
				if fp == nil {
					return false
				}
				n := fp.nodeByHash[bestHash]
				return n != nil && !n.requested
			},
			request: func(dp distPeer) func() {
				p := dp.(*peer)
				f.lock.Lock()
				fp := f.peers[p]
				if fp != nil {
					n := fp.nodeByHash[bestHash]
					if n != nil {
						n.requested = true
					}
				}
				f.lock.Unlock()

				cost := p.GetRequestCost(GetBlockHeadersMsg, int(bestAmount))
				p.fcServer.QueueRequest(reqID, cost)
				f.reqMu.Lock()
				f.requested[reqID] = fetchRequest{hash: bestHash, amount: bestAmount, peer: p, sent: mclock.Now()}
				f.reqMu.Unlock()
				go func() {
					time.Sleep(hardRequestTimeout)
					f.timeoutChn <- reqID
				}()
				return func() { p.RequestHeadersByHash(reqID, cost, bestHash, int(bestAmount), 0, true) }
			},
		}
	}
	return rq, reqID
}

func (f *lightFetcher) deliverHeaders(peer *peer, reqID uint64, headers []*types.Header) {
	f.deliverChn <- fetchResponse{reqID: reqID, headers: headers, peer: peer}
}

func (f *lightFetcher) processResponse(req fetchRequest, resp fetchResponse) bool {
	if uint64(len(resp.headers)) != req.amount || resp.headers[0].Hash() != req.hash {
		req.peer.Log().Debug("Response content mismatch", "requested", len(resp.headers), "reqfrom", resp.headers[0], "delivered", req.amount, "delfrom", req.hash)
		return false
	}
	headers := make([]*types.Header, req.amount)
	for i, header := range resp.headers {
		headers[int(req.amount)-1-i] = header
	}
	if _, err := f.chain.InsertHeaderChain(headers, 1); err != nil {
		if err == consensus.ErrFutureBlock {
			return true
		}
		log4j.Debug("Failed to insert header chain", "err", err)
		return false
	}
	tds := make([]*big.Int, len(headers))
	for i, header := range headers {
		td := f.chain.GetTd(header.Hash(), header.Number.Uint64())
		if td == nil {
			log4j.Debug("Total difficulty not found for header", "index", i+1, "number", header.Number, "hash", header.Hash())
			return false
		}
		tds[i] = td
	}
	f.newHeaders(headers, tds)
	return true
}

func (f *lightFetcher) newHeaders(headers []*types.Header, tds []*big.Int) {
	var maxTd *big.Int
	for p, fp := range f.peers {
		if !f.checkAnnouncedHeaders(fp, headers, tds) {
			p.Log().Debug("Inconsistent announcement")
			go f.pm.removePeer(p.id)
		}
		if fp.confirmedTd != nil && (maxTd == nil || maxTd.Cmp(fp.confirmedTd) > 0) {
			maxTd = fp.confirmedTd
		}
	}
	if maxTd != nil {
		f.updateMaxConfirmedTd(maxTd)
	}
}

func (f *lightFetcher) checkAnnouncedHeaders(fp *fetcherPeerInfo, headers []*types.Header, tds []*big.Int) bool {
	var (
		n      *fetcherTreeNode
		header *types.Header
		td     *big.Int
	)

	for i := len(headers) - 1; ; i-- {
		if i < 0 {
			if n == nil {
				return true
			}
			hash, number := header.ParentHash, header.Number.Uint64()-1
			td = f.chain.GetTd(hash, number)
			header = f.chain.GetHeader(hash, number)
			if header == nil || td == nil {
				log4j.Error("Missing parent of validated header", "hash", hash, "number", number)
				return false
			}
		} else {
			header = headers[i]
			td = tds[i]
		}
		hash := header.Hash()
		number := header.Number.Uint64()
		if n == nil {
			n = fp.nodeByHash[hash]
		}
		if n != nil {
			if n.td == nil {
				if nn := fp.nodeByHash[hash]; nn != nil {
					nn.children = append(nn.children, n.children...)
					n.children = nil
					fp.deleteNode(n)
					n = nn
				} else {
					n.hash = hash
					n.td = td
					fp.nodeByHash[hash] = n
				}
			}
			if n.hash != hash || n.number != number || n.td.Cmp(td) != 0 {
				return false
			}
			if n.known {
				return true
			}
			n.known = true
			if fp.confirmedTd == nil || td.Cmp(fp.confirmedTd) > 0 {
				fp.confirmedTd = td
				fp.bestConfirmed = n
			}
			n = n.parent
			if n == nil {
				return true
			}
		}
	}
}

func (f *lightFetcher) checkSyncedHeaders(p *peer) {
	fp := f.peers[p]
	if fp == nil {
		p.Log().Debug("Unknown peer to check sync headers")
		return
	}
	n := fp.lastAnnounced
	var td *big.Int
	for n != nil {
		if td = f.chain.GetTd(n.hash, n.number); td != nil {
			break
		}
		n = n.parent
	}
	if n == nil {
		p.Log().Debug("Synchronisation failed")
		go f.pm.removePeer(p.id)
	} else {
		header := f.chain.GetHeader(n.hash, n.number)
		f.newHeaders([]*types.Header{header}, []*big.Int{td})
	}
}

func (f *lightFetcher) checkKnownNode(p *peer, n *fetcherTreeNode) bool {
	if n.known {
		return true
	}
	td := f.chain.GetTd(n.hash, n.number)
	if td == nil {
		return false
	}
	header := f.chain.GetHeader(n.hash, n.number)
	if header == nil {
		return false
	}

	fp := f.peers[p]
	if fp == nil {
		p.Log().Debug("Unknown peer to check known nodes")
		return false
	}
	if !f.checkAnnouncedHeaders(fp, []*types.Header{header}, []*big.Int{td}) {
		p.Log().Debug("Inconsistent announcement")
		go f.pm.removePeer(p.id)
	}
	if fp.confirmedTd != nil {
		f.updateMaxConfirmedTd(fp.confirmedTd)
	}
	return n.known
}

func (fp *fetcherPeerInfo) deleteNode(n *fetcherTreeNode) {
	if n.parent != nil {
		for i, nn := range n.parent.children {
			if nn == n {
				n.parent.children = append(n.parent.children[:i], n.parent.children[i+1:]...)
				break
			}
		}
	}
	for {
		if n.td != nil {
			delete(fp.nodeByHash, n.hash)
		}
		fp.nodeCnt--
		if len(n.children) == 0 {
			return
		}
		for i, nn := range n.children {
			if i == 0 {
				n = nn
			} else {
				fp.deleteNode(nn)
			}
		}
	}
}

type updateStatsEntry struct {
	time mclock.AbsTime
	td   *big.Int
	next *updateStatsEntry
}

func (f *lightFetcher) updateMaxConfirmedTd(td *big.Int) {
	if f.maxConfirmedTd == nil || td.Cmp(f.maxConfirmedTd) > 0 {
		f.maxConfirmedTd = td
		newEntry := &updateStatsEntry{
			time: mclock.Now(),
			td:   td,
		}
		if f.lastUpdateStats != nil {
			f.lastUpdateStats.next = newEntry
		}
		f.lastUpdateStats = newEntry
		for p := range f.peers {
			f.checkUpdateStats(p, newEntry)
		}
	}
}

func (f *lightFetcher) checkUpdateStats(p *peer, newEntry *updateStatsEntry) {
	now := mclock.Now()
	fp := f.peers[p]
	if fp == nil {
		p.Log().Debug("Unknown peer to check update stats")
		return
	}
	if newEntry != nil && fp.firstUpdateStats == nil {
		fp.firstUpdateStats = newEntry
	}
	for fp.firstUpdateStats != nil && fp.firstUpdateStats.time <= now-mclock.AbsTime(blockDelayTimeout) {
		f.pm.serverPool.adjustBlockDelay(p.poolEntry, blockDelayTimeout)
		fp.firstUpdateStats = fp.firstUpdateStats.next
	}
	if fp.confirmedTd != nil {
		for fp.firstUpdateStats != nil && fp.firstUpdateStats.td.Cmp(fp.confirmedTd) <= 0 {
			f.pm.serverPool.adjustBlockDelay(p.poolEntry, time.Duration(now-fp.firstUpdateStats.time))
			fp.firstUpdateStats = fp.firstUpdateStats.next
		}
	}
}
