package lcs

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/mclock"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/discv5"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/lib/trie"
	"github.com/5uwifi/canchain/light"
	"github.com/5uwifi/canchain/params"
)

const (
	softResponseLimit = 2 * 1024 * 1024
	estHeaderRlpSize  = 500

	ethVersion = 63

	MaxHeaderFetch           = 192
	MaxBodyFetch             = 32
	MaxReceiptFetch          = 128
	MaxCodeFetch             = 64
	MaxProofsFetch           = 64
	MaxHelperTrieProofsFetch = 64
	MaxTxSend                = 64
	MaxTxStatus              = 256

	disableClientRemovePeer = false
)

func errResp(code errCode, format string, v ...interface{}) error {
	return fmt.Errorf("%v - %v", code, fmt.Sprintf(format, v...))
}

type BlockChain interface {
	Config() *params.ChainConfig
	HasHeader(hash common.Hash, number uint64) bool
	GetHeader(hash common.Hash, number uint64) *types.Header
	GetHeaderByHash(hash common.Hash) *types.Header
	CurrentHeader() *types.Header
	GetTd(hash common.Hash, number uint64) *big.Int
	State() (*state.StateDB, error)
	InsertHeaderChain(chain []*types.Header, checkFreq int) (int, error)
	Rollback(chain []common.Hash)
	GetHeaderByNumber(number uint64) *types.Header
	GetAncestor(hash common.Hash, number, ancestor uint64, maxNonCanonical *uint64) (common.Hash, uint64)
	Genesis() *types.Block
	SubscribeChainHeadEvent(ch chan<- kernel.ChainHeadEvent) event.Subscription
}

type txPool interface {
	AddRemotes(txs []*types.Transaction) []error
	Status(hashes []common.Hash) []kernel.TxStatus
}

type ProtocolManager struct {
	lightSync   bool
	txpool      txPool
	txrelay     *LesTxRelay
	networkId   uint64
	chainConfig *params.ChainConfig
	iConfig     *light.IndexerConfig
	blockchain  BlockChain
	chainDb     candb.Database
	odr         *LesOdr
	server      *LcsServer
	serverPool  *serverPool
	clientPool  *freeClientPool
	lesTopic    discv5.Topic
	reqDist     *requestDistributor
	retriever   *retrieveManager

	downloader *downloader.Downloader
	fetcher    *lightFetcher
	peers      *peerSet
	maxPeers   int

	eventMux *event.TypeMux

	newPeerCh   chan *peer
	quitSync    chan struct{}
	noMorePeers chan struct{}

	wg *sync.WaitGroup
}

func NewProtocolManager(chainConfig *params.ChainConfig, indexerConfig *light.IndexerConfig, lightSync bool, networkId uint64, mux *event.TypeMux, engine consensus.Engine, peers *peerSet, blockchain BlockChain, txpool txPool, chainDb candb.Database, odr *LesOdr, txrelay *LesTxRelay, serverPool *serverPool, quitSync chan struct{}, wg *sync.WaitGroup) (*ProtocolManager, error) {
	manager := &ProtocolManager{
		lightSync:   lightSync,
		eventMux:    mux,
		blockchain:  blockchain,
		chainConfig: chainConfig,
		iConfig:     indexerConfig,
		chainDb:     chainDb,
		odr:         odr,
		networkId:   networkId,
		txpool:      txpool,
		txrelay:     txrelay,
		serverPool:  serverPool,
		peers:       peers,
		newPeerCh:   make(chan *peer),
		quitSync:    quitSync,
		wg:          wg,
		noMorePeers: make(chan struct{}),
	}
	if odr != nil {
		manager.retriever = odr.retriever
		manager.reqDist = odr.retriever.dist
	}

	removePeer := manager.removePeer
	if disableClientRemovePeer {
		removePeer = func(id string) {}
	}

	if lightSync {
		manager.downloader = downloader.New(downloader.LightSync, chainDb, manager.eventMux, nil, blockchain, removePeer)
		manager.peers.notify((*downloaderPeerNotify)(manager))
		manager.fetcher = newLightFetcher(manager)
	}

	return manager, nil
}

func (pm *ProtocolManager) removePeer(id string) {
	pm.peers.Unregister(id)
}

func (pm *ProtocolManager) Start(maxPeers int) {
	pm.maxPeers = maxPeers

	if pm.lightSync {
		go pm.syncer()
	} else {
		pm.clientPool = newFreeClientPool(pm.chainDb, maxPeers, 10000, mclock.System{})
		go func() {
			for range pm.newPeerCh {
			}
		}()
	}
}

func (pm *ProtocolManager) Stop() {
	log4j.Info("Stopping light CANChain protocol")

	pm.noMorePeers <- struct{}{}

	close(pm.quitSync)
	if pm.clientPool != nil {
		pm.clientPool.stop()
	}

	pm.peers.Close()

	pm.wg.Wait()

	log4j.Info("Light CANChain protocol stopped")
}

func (pm *ProtocolManager) runPeer(version uint, p *p2p.Peer, rw p2p.MsgReadWriter) error {
	var entry *poolEntry
	peer := pm.newPeer(int(version), pm.networkId, p, rw)
	if pm.serverPool != nil {
		addr := p.RemoteAddr().(*net.TCPAddr)
		entry = pm.serverPool.connect(peer, addr.IP, uint16(addr.Port))
	}
	peer.poolEntry = entry
	select {
	case pm.newPeerCh <- peer:
		pm.wg.Add(1)
		defer pm.wg.Done()
		err := pm.handle(peer)
		if entry != nil {
			pm.serverPool.disconnect(entry)
		}
		return err
	case <-pm.quitSync:
		if entry != nil {
			pm.serverPool.disconnect(entry)
		}
		return p2p.DiscQuitting
	}
}

func (pm *ProtocolManager) newPeer(pv int, nv uint64, p *p2p.Peer, rw p2p.MsgReadWriter) *peer {
	return newPeer(pv, nv, p, newMeteredMsgWriter(rw))
}

func (pm *ProtocolManager) handle(p *peer) error {
	if pm.lightSync && pm.peers.Len() >= pm.maxPeers && !p.Peer.Info().Network.Trusted {
		return p2p.DiscTooManyPeers
	}

	p.Log().Debug("Light CANChain peer connected", "name", p.Name())

	var (
		genesis = pm.blockchain.Genesis()
		head    = pm.blockchain.CurrentHeader()
		hash    = head.Hash()
		number  = head.Number.Uint64()
		td      = pm.blockchain.GetTd(hash, number)
	)
	if err := p.Handshake(td, hash, number, genesis.Hash(), pm.server); err != nil {
		p.Log().Debug("Light CANChain handshake failed", "err", err)
		return err
	}

	if !pm.lightSync && !p.Peer.Info().Network.Trusted {
		addr, ok := p.RemoteAddr().(*net.TCPAddr)
		if ok {
			id := addr.IP.String()
			if !pm.clientPool.connect(id, func() { go pm.removePeer(p.id) }) {
				return p2p.DiscTooManyPeers
			}
			defer pm.clientPool.disconnect(id)
		}
	}

	if rw, ok := p.rw.(*meteredMsgReadWriter); ok {
		rw.Init(p.version)
	}
	if err := pm.peers.Register(p); err != nil {
		p.Log().Error("Light CANChain peer registration failed", "err", err)
		return err
	}
	defer func() {
		if pm.server != nil && pm.server.fcManager != nil && p.fcClient != nil {
			p.fcClient.Remove(pm.server.fcManager)
		}
		pm.removePeer(p.id)
	}()
	if pm.lightSync {
		p.lock.Lock()
		head := p.headInfo
		p.lock.Unlock()
		if pm.fetcher != nil {
			pm.fetcher.announce(p, head)
		}

		if p.poolEntry != nil {
			pm.serverPool.registered(p.poolEntry)
		}
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case announce := <-p.announceChn:
				p.SendAnnounce(announce)
			case <-stop:
				return
			}
		}
	}()

	for {
		if err := pm.handleMsg(p); err != nil {
			p.Log().Debug("Light CANChain message handling failed", "err", err)
			return err
		}
	}
}

var reqList = []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetCodeMsg, GetReceiptsMsg, GetProofsV1Msg, SendTxMsg, SendTxV2Msg, GetTxStatusMsg, GetHeaderProofsMsg, GetProofsV2Msg, GetHelperTrieProofsMsg}

func (pm *ProtocolManager) handleMsg(p *peer) error {
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	p.Log().Trace("Light CANChain message arrived", "code", msg.Code, "bytes", msg.Size)

	costs := p.fcCosts[msg.Code]
	reject := func(reqCnt, maxCnt uint64) bool {
		if p.fcClient == nil || reqCnt > maxCnt {
			return true
		}
		bufValue, _ := p.fcClient.AcceptRequest()
		cost := costs.baseCost + reqCnt*costs.reqCost
		if cost > pm.server.defParams.BufLimit {
			cost = pm.server.defParams.BufLimit
		}
		if cost > bufValue {
			recharge := time.Duration((cost - bufValue) * 1000000 / pm.server.defParams.MinRecharge)
			p.Log().Error("Request came too early", "recharge", common.PrettyDuration(recharge))
			return true
		}
		return false
	}

	if msg.Size > ProtocolMaxMsgSize {
		return errResp(ErrMsgTooLarge, "%v > %v", msg.Size, ProtocolMaxMsgSize)
	}
	defer msg.Discard()

	var deliverMsg *Msg

	switch msg.Code {
	case StatusMsg:
		p.Log().Trace("Received status message")
		return errResp(ErrExtraStatusMsg, "uncontrolled status message")

	case AnnounceMsg:
		p.Log().Trace("Received announce message")
		if p.requestAnnounceType == announceTypeNone {
			return errResp(ErrUnexpectedResponse, "")
		}

		var req announceData
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "%v: %v", msg, err)
		}

		if p.requestAnnounceType == announceTypeSigned {
			if err := req.checkSignature(p.pubKey); err != nil {
				p.Log().Trace("Invalid announcement signature", "err", err)
				return err
			}
			p.Log().Trace("Valid announcement signature")
		}

		p.Log().Trace("Announce message content", "number", req.Number, "hash", req.Hash, "td", req.Td, "reorg", req.ReorgDepth)
		if pm.fetcher != nil {
			pm.fetcher.announce(p, &req)
		}

	case GetBlockHeadersMsg:
		p.Log().Trace("Received block header request")
		var req struct {
			ReqID uint64
			Query getBlockHeadersData
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "%v: %v", msg, err)
		}

		query := req.Query
		if reject(query.Amount, MaxHeaderFetch) {
			return errResp(ErrRequestRejected, "")
		}

		hashMode := query.Origin.Hash != (common.Hash{})
		first := true
		maxNonCanonical := uint64(100)

		var (
			bytes   common.StorageSize
			headers []*types.Header
			unknown bool
		)
		for !unknown && len(headers) < int(query.Amount) && bytes < softResponseLimit {
			var origin *types.Header
			if hashMode {
				if first {
					first = false
					origin = pm.blockchain.GetHeaderByHash(query.Origin.Hash)
					if origin != nil {
						query.Origin.Number = origin.Number.Uint64()
					}
				} else {
					origin = pm.blockchain.GetHeader(query.Origin.Hash, query.Origin.Number)
				}
			} else {
				origin = pm.blockchain.GetHeaderByNumber(query.Origin.Number)
			}
			if origin == nil {
				break
			}
			headers = append(headers, origin)
			bytes += estHeaderRlpSize

			switch {
			case hashMode && query.Reverse:
				ancestor := query.Skip + 1
				if ancestor == 0 {
					unknown = true
				} else {
					query.Origin.Hash, query.Origin.Number = pm.blockchain.GetAncestor(query.Origin.Hash, query.Origin.Number, ancestor, &maxNonCanonical)
					unknown = (query.Origin.Hash == common.Hash{})
				}
			case hashMode && !query.Reverse:
				var (
					current = origin.Number.Uint64()
					next    = current + query.Skip + 1
				)
				if next <= current {
					infos, _ := json.MarshalIndent(p.Peer.Info(), "", "  ")
					p.Log().Warn("GetBlockHeaders skip overflow attack", "current", current, "skip", query.Skip, "next", next, "attacker", infos)
					unknown = true
				} else {
					if header := pm.blockchain.GetHeaderByNumber(next); header != nil {
						nextHash := header.Hash()
						expOldHash, _ := pm.blockchain.GetAncestor(nextHash, next, query.Skip+1, &maxNonCanonical)
						if expOldHash == query.Origin.Hash {
							query.Origin.Hash, query.Origin.Number = nextHash, next
						} else {
							unknown = true
						}
					} else {
						unknown = true
					}
				}
			case query.Reverse:
				if query.Origin.Number >= query.Skip+1 {
					query.Origin.Number -= query.Skip + 1
				} else {
					unknown = true
				}

			case !query.Reverse:
				query.Origin.Number += query.Skip + 1
			}
		}

		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + query.Amount*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, query.Amount, rcost)
		return p.SendBlockHeaders(req.ReqID, bv, headers)

	case BlockHeadersMsg:
		if pm.downloader == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received block header response message")
		var resp struct {
			ReqID, BV uint64
			Headers   []*types.Header
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		if pm.fetcher != nil && pm.fetcher.requestedID(resp.ReqID) {
			pm.fetcher.deliverHeaders(p, resp.ReqID, resp.Headers)
		} else {
			err := pm.downloader.DeliverHeaders(p.id, resp.Headers)
			if err != nil {
				log4j.Debug(fmt.Sprint(err))
			}
		}

	case GetBlockBodiesMsg:
		p.Log().Trace("Received block bodies request")
		var req struct {
			ReqID  uint64
			Hashes []common.Hash
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		var (
			bytes  int
			bodies []rlp.RawValue
		)
		reqCnt := len(req.Hashes)
		if reject(uint64(reqCnt), MaxBodyFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, hash := range req.Hashes {
			if bytes >= softResponseLimit {
				break
			}
			if number := rawdb.ReadHeaderNumber(pm.chainDb, hash); number != nil {
				if data := rawdb.ReadBodyRLP(pm.chainDb, hash, *number); len(data) != 0 {
					bodies = append(bodies, data)
					bytes += len(data)
				}
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendBlockBodiesRLP(req.ReqID, bv, bodies)

	case BlockBodiesMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received block bodies response")
		var resp struct {
			ReqID, BV uint64
			Data      []*types.Body
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgBlockBodies,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case GetCodeMsg:
		p.Log().Trace("Received code request")
		var req struct {
			ReqID uint64
			Reqs  []CodeReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		var (
			bytes int
			data  [][]byte
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxCodeFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, req := range req.Reqs {
			if number := rawdb.ReadHeaderNumber(pm.chainDb, req.BHash); number != nil {
				if header := rawdb.ReadHeader(pm.chainDb, req.BHash, *number); header != nil {
					statedb, err := pm.blockchain.State()
					if err != nil {
						continue
					}
					account, err := pm.getAccount(statedb, header.Root, common.BytesToHash(req.AccKey))
					if err != nil {
						continue
					}
					code, _ := statedb.Database().TrieDB().Node(common.BytesToHash(account.CodeHash))

					data = append(data, code)
					if bytes += len(code); bytes >= softResponseLimit {
						break
					}
				}
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendCode(req.ReqID, bv, data)

	case CodeMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received code response")
		var resp struct {
			ReqID, BV uint64
			Data      [][]byte
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgCode,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case GetReceiptsMsg:
		p.Log().Trace("Received receipts request")
		var req struct {
			ReqID  uint64
			Hashes []common.Hash
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		var (
			bytes    int
			receipts []rlp.RawValue
		)
		reqCnt := len(req.Hashes)
		if reject(uint64(reqCnt), MaxReceiptFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, hash := range req.Hashes {
			if bytes >= softResponseLimit {
				break
			}
			var results types.Receipts
			if number := rawdb.ReadHeaderNumber(pm.chainDb, hash); number != nil {
				results = rawdb.ReadReceipts(pm.chainDb, hash, *number)
			}
			if results == nil {
				if header := pm.blockchain.GetHeaderByHash(hash); header == nil || header.ReceiptHash != types.EmptyRootHash {
					continue
				}
			}
			if encoded, err := rlp.EncodeToBytes(results); err != nil {
				log4j.Error("Failed to encode receipt", "err", err)
			} else {
				receipts = append(receipts, encoded)
				bytes += len(encoded)
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendReceiptsRLP(req.ReqID, bv, receipts)

	case ReceiptsMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received receipts response")
		var resp struct {
			ReqID, BV uint64
			Receipts  []types.Receipts
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgReceipts,
			ReqID:   resp.ReqID,
			Obj:     resp.Receipts,
		}

	case GetProofsV1Msg:
		p.Log().Trace("Received proofs request")
		var req struct {
			ReqID uint64
			Reqs  []ProofReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		var (
			bytes  int
			proofs proofsData
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, req := range req.Reqs {
			if number := rawdb.ReadHeaderNumber(pm.chainDb, req.BHash); number != nil {
				if header := rawdb.ReadHeader(pm.chainDb, req.BHash, *number); header != nil {
					statedb, err := pm.blockchain.State()
					if err != nil {
						continue
					}
					var trie state.Trie
					if len(req.AccKey) > 0 {
						account, err := pm.getAccount(statedb, header.Root, common.BytesToHash(req.AccKey))
						if err != nil {
							continue
						}
						trie, _ = statedb.Database().OpenStorageTrie(common.BytesToHash(req.AccKey), account.Root)
					} else {
						trie, _ = statedb.Database().OpenTrie(header.Root)
					}
					if trie != nil {
						var proof light.NodeList
						trie.Prove(req.Key, 0, &proof)

						proofs = append(proofs, proof)
						if bytes += proof.DataSize(); bytes >= softResponseLimit {
							break
						}
					}
				}
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendProofs(req.ReqID, bv, proofs)

	case GetProofsV2Msg:
		p.Log().Trace("Received les/2 proofs request")
		var req struct {
			ReqID uint64
			Reqs  []ProofReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		var (
			lastBHash common.Hash
			statedb   *state.StateDB
			root      common.Hash
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}

		nodes := light.NewNodeSet()

		for _, req := range req.Reqs {
			if statedb == nil || req.BHash != lastBHash {
				statedb, root, lastBHash = nil, common.Hash{}, req.BHash

				if number := rawdb.ReadHeaderNumber(pm.chainDb, req.BHash); number != nil {
					if header := rawdb.ReadHeader(pm.chainDb, req.BHash, *number); header != nil {
						statedb, _ = pm.blockchain.State()
						root = header.Root
					}
				}
			}
			if statedb == nil {
				continue
			}
			var trie state.Trie
			if len(req.AccKey) > 0 {
				account, err := pm.getAccount(statedb, root, common.BytesToHash(req.AccKey))
				if err != nil {
					continue
				}
				trie, _ = statedb.Database().OpenStorageTrie(common.BytesToHash(req.AccKey), account.Root)
			} else {
				trie, _ = statedb.Database().OpenTrie(root)
			}
			if trie == nil {
				continue
			}
			trie.Prove(req.Key, req.FromLevel, nodes)
			if nodes.DataSize() >= softResponseLimit {
				break
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendProofsV2(req.ReqID, bv, nodes.NodeList())

	case ProofsV1Msg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received proofs response")
		var resp struct {
			ReqID, BV uint64
			Data      []light.NodeList
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgProofsV1,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case ProofsV2Msg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received les/2 proofs response")
		var resp struct {
			ReqID, BV uint64
			Data      light.NodeList
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgProofsV2,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case GetHeaderProofsMsg:
		p.Log().Trace("Received headers proof request")
		var req struct {
			ReqID uint64
			Reqs  []ChtReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		var (
			bytes  int
			proofs []ChtResp
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxHelperTrieProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}
		trieDb := trie.NewDatabase(candb.NewTable(pm.chainDb, light.ChtTablePrefix))
		for _, req := range req.Reqs {
			if header := pm.blockchain.GetHeaderByNumber(req.BlockNum); header != nil {
				sectionHead := rawdb.ReadCanonicalHash(pm.chainDb, req.ChtNum*pm.iConfig.ChtSize-1)
				if root := light.GetChtRoot(pm.chainDb, req.ChtNum-1, sectionHead); root != (common.Hash{}) {
					trie, err := trie.New(root, trieDb)
					if err != nil {
						continue
					}
					var encNumber [8]byte
					binary.BigEndian.PutUint64(encNumber[:], req.BlockNum)

					var proof light.NodeList
					trie.Prove(encNumber[:], 0, &proof)

					proofs = append(proofs, ChtResp{Header: header, Proof: proof})
					if bytes += proof.DataSize() + estHeaderRlpSize; bytes >= softResponseLimit {
						break
					}
				}
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendHeaderProofs(req.ReqID, bv, proofs)

	case GetHelperTrieProofsMsg:
		p.Log().Trace("Received helper trie proof request")
		var req struct {
			ReqID uint64
			Reqs  []HelperTrieReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		var (
			auxBytes int
			auxData  [][]byte
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxHelperTrieProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}

		var (
			lastIdx  uint64
			lastType uint
			root     common.Hash
			auxTrie  *trie.Trie
		)
		nodes := light.NewNodeSet()
		for _, req := range req.Reqs {
			if auxTrie == nil || req.Type != lastType || req.TrieIdx != lastIdx {
				auxTrie, lastType, lastIdx = nil, req.Type, req.TrieIdx

				var prefix string
				if root, prefix = pm.getHelperTrie(req.Type, req.TrieIdx); root != (common.Hash{}) {
					auxTrie, _ = trie.New(root, trie.NewDatabase(candb.NewTable(pm.chainDb, prefix)))
				}
			}
			if req.AuxReq == auxRoot {
				var data []byte
				if root != (common.Hash{}) {
					data = root[:]
				}
				auxData = append(auxData, data)
				auxBytes += len(data)
			} else {
				if auxTrie != nil {
					auxTrie.Prove(req.Key, req.FromLevel, nodes)
				}
				if req.AuxReq != 0 {
					data := pm.getHelperTrieAuxData(req)
					auxData = append(auxData, data)
					auxBytes += len(data)
				}
			}
			if nodes.DataSize()+auxBytes >= softResponseLimit {
				break
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendHelperTrieProofs(req.ReqID, bv, HelperTrieResps{Proofs: nodes.NodeList(), AuxData: auxData})

	case HeaderProofsMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received headers proof response")
		var resp struct {
			ReqID, BV uint64
			Data      []ChtResp
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgHeaderProofs,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case HelperTrieProofsMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received helper trie proof response")
		var resp struct {
			ReqID, BV uint64
			Data      HelperTrieResps
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}

		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgHelperTrieProofs,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case SendTxMsg:
		if pm.txpool == nil {
			return errResp(ErrRequestRejected, "")
		}
		var txs []*types.Transaction
		if err := msg.Decode(&txs); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		reqCnt := len(txs)
		if reject(uint64(reqCnt), MaxTxSend) {
			return errResp(ErrRequestRejected, "")
		}
		pm.txpool.AddRemotes(txs)

		_, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)

	case SendTxV2Msg:
		if pm.txpool == nil {
			return errResp(ErrRequestRejected, "")
		}
		var req struct {
			ReqID uint64
			Txs   []*types.Transaction
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		reqCnt := len(req.Txs)
		if reject(uint64(reqCnt), MaxTxSend) {
			return errResp(ErrRequestRejected, "")
		}

		hashes := make([]common.Hash, len(req.Txs))
		for i, tx := range req.Txs {
			hashes[i] = tx.Hash()
		}
		stats := pm.txStatus(hashes)
		for i, stat := range stats {
			if stat.Status == kernel.TxStatusUnknown {
				if errs := pm.txpool.AddRemotes([]*types.Transaction{req.Txs[i]}); errs[0] != nil {
					stats[i].Error = errs[0].Error()
					continue
				}
				stats[i] = pm.txStatus([]common.Hash{hashes[i]})[0]
			}
		}

		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)

		return p.SendTxStatus(req.ReqID, bv, stats)

	case GetTxStatusMsg:
		if pm.txpool == nil {
			return errResp(ErrUnexpectedResponse, "")
		}
		var req struct {
			ReqID  uint64
			Hashes []common.Hash
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		reqCnt := len(req.Hashes)
		if reject(uint64(reqCnt), MaxTxStatus) {
			return errResp(ErrRequestRejected, "")
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)

		return p.SendTxStatus(req.ReqID, bv, pm.txStatus(req.Hashes))

	case TxStatusMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received tx status response")
		var resp struct {
			ReqID, BV uint64
			Status    []txStatus
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}

		p.fcServer.GotReply(resp.ReqID, resp.BV)

	default:
		p.Log().Trace("Received unknown message", "code", msg.Code)
		return errResp(ErrInvalidMsgCode, "%v", msg.Code)
	}

	if deliverMsg != nil {
		err := pm.retriever.deliver(p, deliverMsg)
		if err != nil {
			p.responseErrors++
			if p.responseErrors > maxResponseErrors {
				return err
			}
		}
	}
	return nil
}

func (pm *ProtocolManager) getAccount(statedb *state.StateDB, root, hash common.Hash) (state.Account, error) {
	trie, err := trie.New(root, statedb.Database().TrieDB())
	if err != nil {
		return state.Account{}, err
	}
	blob, err := trie.TryGet(hash[:])
	if err != nil {
		return state.Account{}, err
	}
	var account state.Account
	if err = rlp.DecodeBytes(blob, &account); err != nil {
		return state.Account{}, err
	}
	return account, nil
}

func (pm *ProtocolManager) getHelperTrie(id uint, idx uint64) (common.Hash, string) {
	switch id {
	case htCanonical:
		idxV1 := (idx+1)*(pm.iConfig.PairChtSize/pm.iConfig.ChtSize) - 1
		sectionHead := rawdb.ReadCanonicalHash(pm.chainDb, (idxV1+1)*pm.iConfig.ChtSize-1)
		return light.GetChtRoot(pm.chainDb, idxV1, sectionHead), light.ChtTablePrefix
	case htBloomBits:
		sectionHead := rawdb.ReadCanonicalHash(pm.chainDb, (idx+1)*pm.iConfig.BloomTrieSize-1)
		return light.GetBloomTrieRoot(pm.chainDb, idx, sectionHead), light.BloomTrieTablePrefix
	}
	return common.Hash{}, ""
}

func (pm *ProtocolManager) getHelperTrieAuxData(req HelperTrieReq) []byte {
	if req.Type == htCanonical && req.AuxReq == auxHeader && len(req.Key) == 8 {
		blockNum := binary.BigEndian.Uint64(req.Key)
		hash := rawdb.ReadCanonicalHash(pm.chainDb, blockNum)
		return rawdb.ReadHeaderRLP(pm.chainDb, hash, blockNum)
	}
	return nil
}

func (pm *ProtocolManager) txStatus(hashes []common.Hash) []txStatus {
	stats := make([]txStatus, len(hashes))
	for i, stat := range pm.txpool.Status(hashes) {
		stats[i].Status = stat

		if stat == kernel.TxStatusUnknown {
			if block, number, index := rawdb.ReadTxLookupEntry(pm.chainDb, hashes[i]); block != (common.Hash{}) {
				stats[i].Status = kernel.TxStatusIncluded
				stats[i].Lookup = &rawdb.TxLookupEntry{BlockHash: block, BlockIndex: number, Index: index}
			}
		}
	}
	return stats
}

type downloaderPeerNotify ProtocolManager

type peerConnection struct {
	manager *ProtocolManager
	peer    *peer
}

func (pc *peerConnection) Head() (common.Hash, *big.Int) {
	return pc.peer.HeadAndTd()
}

func (pc *peerConnection) RequestHeadersByHash(origin common.Hash, amount int, skip int, reverse bool) error {
	reqID := genReqID()
	rq := &distReq{
		getCost: func(dp distPeer) uint64 {
			peer := dp.(*peer)
			return peer.GetRequestCost(GetBlockHeadersMsg, amount)
		},
		canSend: func(dp distPeer) bool {
			return dp.(*peer) == pc.peer
		},
		request: func(dp distPeer) func() {
			peer := dp.(*peer)
			cost := peer.GetRequestCost(GetBlockHeadersMsg, amount)
			peer.fcServer.QueueRequest(reqID, cost)
			return func() { peer.RequestHeadersByHash(reqID, cost, origin, amount, skip, reverse) }
		},
	}
	_, ok := <-pc.manager.reqDist.queue(rq)
	if !ok {
		return light.ErrNoPeers
	}
	return nil
}

func (pc *peerConnection) RequestHeadersByNumber(origin uint64, amount int, skip int, reverse bool) error {
	reqID := genReqID()
	rq := &distReq{
		getCost: func(dp distPeer) uint64 {
			peer := dp.(*peer)
			return peer.GetRequestCost(GetBlockHeadersMsg, amount)
		},
		canSend: func(dp distPeer) bool {
			return dp.(*peer) == pc.peer
		},
		request: func(dp distPeer) func() {
			peer := dp.(*peer)
			cost := peer.GetRequestCost(GetBlockHeadersMsg, amount)
			peer.fcServer.QueueRequest(reqID, cost)
			return func() { peer.RequestHeadersByNumber(reqID, cost, origin, amount, skip, reverse) }
		},
	}
	_, ok := <-pc.manager.reqDist.queue(rq)
	if !ok {
		return light.ErrNoPeers
	}
	return nil
}

func (d *downloaderPeerNotify) registerPeer(p *peer) {
	pm := (*ProtocolManager)(d)
	pc := &peerConnection{
		manager: pm,
		peer:    p,
	}
	pm.downloader.RegisterLightPeer(p.id, ethVersion, pc)
}

func (d *downloaderPeerNotify) unregisterPeer(p *peer) {
	pm := (*ProtocolManager)(d)
	pm.downloader.UnregisterPeer(p.id)
}
