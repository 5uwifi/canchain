package lcs

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/5uwifi/canchain/can"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lcs/flowcontrol"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/light"
)

var (
	errClosed             = errors.New("peer set is closed")
	errAlreadyRegistered  = errors.New("peer is already registered")
	errNotRegistered      = errors.New("peer is not registered")
	errInvalidHelpTrieReq = errors.New("invalid help trie request")
)

const maxResponseErrors = 50

const (
	announceTypeNone = iota
	announceTypeSimple
	announceTypeSigned
)

type peer struct {
	*p2p.Peer

	rw p2p.MsgReadWriter

	version int
	network uint64

	announceType, requestAnnounceType uint64

	id string

	headInfo *announceData
	lock     sync.RWMutex

	announceChn chan announceData
	sendQueue   *execQueue

	poolEntry      *poolEntry
	hasBlock       func(common.Hash, uint64) bool
	responseErrors int

	fcClient       *flowcontrol.ClientNode
	fcServer       *flowcontrol.ServerNode
	fcServerParams *flowcontrol.ServerParams
	fcCosts        requestCostTable
}

func newPeer(version int, network uint64, p *p2p.Peer, rw p2p.MsgReadWriter) *peer {
	id := p.ID()

	return &peer{
		Peer:        p,
		rw:          rw,
		version:     version,
		network:     network,
		id:          fmt.Sprintf("%x", id[:8]),
		announceChn: make(chan announceData, 20),
	}
}

func (p *peer) canQueue() bool {
	return p.sendQueue.canQueue()
}

func (p *peer) queueSend(f func()) {
	p.sendQueue.queue(f)
}

func (p *peer) Info() *can.PeerInfo {
	return &can.PeerInfo{
		Version:    p.version,
		Difficulty: p.Td(),
		Head:       fmt.Sprintf("%x", p.Head()),
	}
}

func (p *peer) Head() (hash common.Hash) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	copy(hash[:], p.headInfo.Hash[:])
	return hash
}

func (p *peer) HeadAndTd() (hash common.Hash, td *big.Int) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	copy(hash[:], p.headInfo.Hash[:])
	return hash, p.headInfo.Td
}

func (p *peer) headBlockInfo() blockInfo {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return blockInfo{Hash: p.headInfo.Hash, Number: p.headInfo.Number, Td: p.headInfo.Td}
}

func (p *peer) Td() *big.Int {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return new(big.Int).Set(p.headInfo.Td)
}

func (p *peer) waitBefore(maxCost uint64) (time.Duration, float64) {
	return p.fcServer.CanSend(maxCost)
}

func sendRequest(w p2p.MsgWriter, msgcode, reqID, cost uint64, data interface{}) error {
	type req struct {
		ReqID uint64
		Data  interface{}
	}
	return p2p.Send(w, msgcode, req{reqID, data})
}

func sendResponse(w p2p.MsgWriter, msgcode, reqID, bv uint64, data interface{}) error {
	type resp struct {
		ReqID, BV uint64
		Data      interface{}
	}
	return p2p.Send(w, msgcode, resp{reqID, bv, data})
}

func (p *peer) GetRequestCost(msgcode uint64, amount int) uint64 {
	p.lock.RLock()
	defer p.lock.RUnlock()

	cost := p.fcCosts[msgcode].baseCost + p.fcCosts[msgcode].reqCost*uint64(amount)
	if cost > p.fcServerParams.BufLimit {
		cost = p.fcServerParams.BufLimit
	}
	return cost
}

func (p *peer) HasBlock(hash common.Hash, number uint64) bool {
	p.lock.RLock()
	hasBlock := p.hasBlock
	p.lock.RUnlock()
	return hasBlock != nil && hasBlock(hash, number)
}

func (p *peer) SendAnnounce(request announceData) error {
	return p2p.Send(p.rw, AnnounceMsg, request)
}

func (p *peer) SendBlockHeaders(reqID, bv uint64, headers []*types.Header) error {
	return sendResponse(p.rw, BlockHeadersMsg, reqID, bv, headers)
}

func (p *peer) SendBlockBodiesRLP(reqID, bv uint64, bodies []rlp.RawValue) error {
	return sendResponse(p.rw, BlockBodiesMsg, reqID, bv, bodies)
}

func (p *peer) SendCode(reqID, bv uint64, data [][]byte) error {
	return sendResponse(p.rw, CodeMsg, reqID, bv, data)
}

func (p *peer) SendReceiptsRLP(reqID, bv uint64, receipts []rlp.RawValue) error {
	return sendResponse(p.rw, ReceiptsMsg, reqID, bv, receipts)
}

func (p *peer) SendProofs(reqID, bv uint64, proofs proofsData) error {
	return sendResponse(p.rw, ProofsV1Msg, reqID, bv, proofs)
}

func (p *peer) SendProofsV2(reqID, bv uint64, proofs light.NodeList) error {
	return sendResponse(p.rw, ProofsV2Msg, reqID, bv, proofs)
}

func (p *peer) SendHeaderProofs(reqID, bv uint64, proofs []ChtResp) error {
	return sendResponse(p.rw, HeaderProofsMsg, reqID, bv, proofs)
}

func (p *peer) SendHelperTrieProofs(reqID, bv uint64, resp HelperTrieResps) error {
	return sendResponse(p.rw, HelperTrieProofsMsg, reqID, bv, resp)
}

func (p *peer) SendTxStatus(reqID, bv uint64, stats []txStatus) error {
	return sendResponse(p.rw, TxStatusMsg, reqID, bv, stats)
}

func (p *peer) RequestHeadersByHash(reqID, cost uint64, origin common.Hash, amount int, skip int, reverse bool) error {
	p.Log().Debug("Fetching batch of headers", "count", amount, "fromhash", origin, "skip", skip, "reverse", reverse)
	return sendRequest(p.rw, GetBlockHeadersMsg, reqID, cost, &getBlockHeadersData{Origin: hashOrNumber{Hash: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse})
}

func (p *peer) RequestHeadersByNumber(reqID, cost, origin uint64, amount int, skip int, reverse bool) error {
	p.Log().Debug("Fetching batch of headers", "count", amount, "fromnum", origin, "skip", skip, "reverse", reverse)
	return sendRequest(p.rw, GetBlockHeadersMsg, reqID, cost, &getBlockHeadersData{Origin: hashOrNumber{Number: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse})
}

func (p *peer) RequestBodies(reqID, cost uint64, hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of block bodies", "count", len(hashes))
	return sendRequest(p.rw, GetBlockBodiesMsg, reqID, cost, hashes)
}

func (p *peer) RequestCode(reqID, cost uint64, reqs []CodeReq) error {
	p.Log().Debug("Fetching batch of codes", "count", len(reqs))
	return sendRequest(p.rw, GetCodeMsg, reqID, cost, reqs)
}

func (p *peer) RequestReceipts(reqID, cost uint64, hashes []common.Hash) error {
	p.Log().Debug("Fetching batch of receipts", "count", len(hashes))
	return sendRequest(p.rw, GetReceiptsMsg, reqID, cost, hashes)
}

func (p *peer) RequestProofs(reqID, cost uint64, reqs []ProofReq) error {
	p.Log().Debug("Fetching batch of proofs", "count", len(reqs))
	switch p.version {
	case lpv1:
		return sendRequest(p.rw, GetProofsV1Msg, reqID, cost, reqs)
	case lpv2:
		return sendRequest(p.rw, GetProofsV2Msg, reqID, cost, reqs)
	default:
		panic(nil)
	}
}

func (p *peer) RequestHelperTrieProofs(reqID, cost uint64, data interface{}) error {
	switch p.version {
	case lpv1:
		reqs, ok := data.([]ChtReq)
		if !ok {
			return errInvalidHelpTrieReq
		}
		p.Log().Debug("Fetching batch of header proofs", "count", len(reqs))
		return sendRequest(p.rw, GetHeaderProofsMsg, reqID, cost, reqs)
	case lpv2:
		reqs, ok := data.([]HelperTrieReq)
		if !ok {
			return errInvalidHelpTrieReq
		}
		p.Log().Debug("Fetching batch of HelperTrie proofs", "count", len(reqs))
		return sendRequest(p.rw, GetHelperTrieProofsMsg, reqID, cost, reqs)
	default:
		panic(nil)
	}
}

func (p *peer) RequestTxStatus(reqID, cost uint64, txHashes []common.Hash) error {
	p.Log().Debug("Requesting transaction status", "count", len(txHashes))
	return sendRequest(p.rw, GetTxStatusMsg, reqID, cost, txHashes)
}

func (p *peer) SendTxs(reqID, cost uint64, txs types.Transactions) error {
	p.Log().Debug("Fetching batch of transactions", "count", len(txs))
	switch p.version {
	case lpv1:
		return p2p.Send(p.rw, SendTxMsg, txs)
	case lpv2:
		return sendRequest(p.rw, SendTxV2Msg, reqID, cost, txs)
	default:
		panic(nil)
	}
}

type keyValueEntry struct {
	Key   string
	Value rlp.RawValue
}
type keyValueList []keyValueEntry
type keyValueMap map[string]rlp.RawValue

func (l keyValueList) add(key string, val interface{}) keyValueList {
	var entry keyValueEntry
	entry.Key = key
	if val == nil {
		val = uint64(0)
	}
	enc, err := rlp.EncodeToBytes(val)
	if err == nil {
		entry.Value = enc
	}
	return append(l, entry)
}

func (l keyValueList) decode() keyValueMap {
	m := make(keyValueMap)
	for _, entry := range l {
		m[entry.Key] = entry.Value
	}
	return m
}

func (m keyValueMap) get(key string, val interface{}) error {
	enc, ok := m[key]
	if !ok {
		return errResp(ErrMissingKey, "%s", key)
	}
	if val == nil {
		return nil
	}
	return rlp.DecodeBytes(enc, val)
}

func (p *peer) sendReceiveHandshake(sendList keyValueList) (keyValueList, error) {
	errc := make(chan error, 1)
	go func() {
		errc <- p2p.Send(p.rw, StatusMsg, sendList)
	}()
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return nil, err
	}
	if msg.Code != StatusMsg {
		return nil, errResp(ErrNoStatusMsg, "first msg has code %x (!= %x)", msg.Code, StatusMsg)
	}
	if msg.Size > ProtocolMaxMsgSize {
		return nil, errResp(ErrMsgTooLarge, "%v > %v", msg.Size, ProtocolMaxMsgSize)
	}
	var recvList keyValueList
	if err := msg.Decode(&recvList); err != nil {
		return nil, errResp(ErrDecode, "msg %v: %v", msg, err)
	}
	if err := <-errc; err != nil {
		return nil, err
	}
	return recvList, nil
}

func (p *peer) Handshake(td *big.Int, head common.Hash, headNum uint64, genesis common.Hash, server *LcsServer) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	var send keyValueList
	send = send.add("protocolVersion", uint64(p.version))
	send = send.add("networkId", p.network)
	send = send.add("headTd", td)
	send = send.add("headHash", head)
	send = send.add("headNum", headNum)
	send = send.add("genesisHash", genesis)
	if server != nil {
		send = send.add("serveHeaders", nil)
		send = send.add("serveChainSince", uint64(0))
		send = send.add("serveStateSince", uint64(0))
		send = send.add("txRelay", nil)
		send = send.add("flowControl/BL", server.defParams.BufLimit)
		send = send.add("flowControl/MRR", server.defParams.MinRecharge)
		list := server.fcCostStats.getCurrentList()
		send = send.add("flowControl/MRC", list)
		p.fcCosts = list.decode()
	} else {
		p.requestAnnounceType = announceTypeSimple
		send = send.add("announceType", p.requestAnnounceType)
	}
	recvList, err := p.sendReceiveHandshake(send)
	if err != nil {
		return err
	}
	recv := recvList.decode()

	var rGenesis, rHash common.Hash
	var rVersion, rNetwork, rNum uint64
	var rTd *big.Int

	if err := recv.get("protocolVersion", &rVersion); err != nil {
		return err
	}
	if err := recv.get("networkId", &rNetwork); err != nil {
		return err
	}
	if err := recv.get("headTd", &rTd); err != nil {
		return err
	}
	if err := recv.get("headHash", &rHash); err != nil {
		return err
	}
	if err := recv.get("headNum", &rNum); err != nil {
		return err
	}
	if err := recv.get("genesisHash", &rGenesis); err != nil {
		return err
	}

	if rGenesis != genesis {
		return errResp(ErrGenesisBlockMismatch, "%x (!= %x)", rGenesis[:8], genesis[:8])
	}
	if rNetwork != p.network {
		return errResp(ErrNetworkIdMismatch, "%d (!= %d)", rNetwork, p.network)
	}
	if int(rVersion) != p.version {
		return errResp(ErrProtocolVersionMismatch, "%d (!= %d)", rVersion, p.version)
	}
	if server != nil {
		/*if recv.get("serveStateSince", nil) == nil {
			return errResp(ErrUselessPeer, "wanted client, got server")
		}*/
		if recv.get("announceType", &p.announceType) != nil {
			p.announceType = announceTypeSimple
		}
		p.fcClient = flowcontrol.NewClientNode(server.fcManager, server.defParams)
	} else {
		if recv.get("serveChainSince", nil) != nil {
			return errResp(ErrUselessPeer, "peer cannot serve chain")
		}
		if recv.get("serveStateSince", nil) != nil {
			return errResp(ErrUselessPeer, "peer cannot serve state")
		}
		if recv.get("txRelay", nil) != nil {
			return errResp(ErrUselessPeer, "peer cannot relay transactions")
		}
		params := &flowcontrol.ServerParams{}
		if err := recv.get("flowControl/BL", &params.BufLimit); err != nil {
			return err
		}
		if err := recv.get("flowControl/MRR", &params.MinRecharge); err != nil {
			return err
		}
		var MRC RequestCostList
		if err := recv.get("flowControl/MRC", &MRC); err != nil {
			return err
		}
		p.fcServerParams = params
		p.fcServer = flowcontrol.NewServerNode(params)
		p.fcCosts = MRC.decode()
	}

	p.headInfo = &announceData{Td: rTd, Hash: rHash, Number: rNum}
	return nil
}

func (p *peer) String() string {
	return fmt.Sprintf("Peer %s [%s]", p.id,
		fmt.Sprintf("les/%d", p.version),
	)
}

type peerSetNotify interface {
	registerPeer(*peer)
	unregisterPeer(*peer)
}

type peerSet struct {
	peers      map[string]*peer
	lock       sync.RWMutex
	notifyList []peerSetNotify
	closed     bool
}

func newPeerSet() *peerSet {
	return &peerSet{
		peers: make(map[string]*peer),
	}
}

func (ps *peerSet) notify(n peerSetNotify) {
	ps.lock.Lock()
	ps.notifyList = append(ps.notifyList, n)
	peers := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		peers = append(peers, p)
	}
	ps.lock.Unlock()

	for _, p := range peers {
		n.registerPeer(p)
	}
}

func (ps *peerSet) Register(p *peer) error {
	ps.lock.Lock()
	if ps.closed {
		ps.lock.Unlock()
		return errClosed
	}
	if _, ok := ps.peers[p.id]; ok {
		ps.lock.Unlock()
		return errAlreadyRegistered
	}
	ps.peers[p.id] = p
	p.sendQueue = newExecQueue(100)
	peers := make([]peerSetNotify, len(ps.notifyList))
	copy(peers, ps.notifyList)
	ps.lock.Unlock()

	for _, n := range peers {
		n.registerPeer(p)
	}
	return nil
}

func (ps *peerSet) Unregister(id string) error {
	ps.lock.Lock()
	if p, ok := ps.peers[id]; !ok {
		ps.lock.Unlock()
		return errNotRegistered
	} else {
		delete(ps.peers, id)
		peers := make([]peerSetNotify, len(ps.notifyList))
		copy(peers, ps.notifyList)
		ps.lock.Unlock()

		for _, n := range peers {
			n.unregisterPeer(p)
		}
		p.sendQueue.quit()
		p.Peer.Disconnect(p2p.DiscUselessPeer)
		return nil
	}
}

func (ps *peerSet) AllPeerIDs() []string {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	res := make([]string, len(ps.peers))
	idx := 0
	for id := range ps.peers {
		res[idx] = id
		idx++
	}
	return res
}

func (ps *peerSet) Peer(id string) *peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	return ps.peers[id]
}

func (ps *peerSet) Len() int {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	return len(ps.peers)
}

func (ps *peerSet) BestPeer() *peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	var (
		bestPeer *peer
		bestTd   *big.Int
	)
	for _, p := range ps.peers {
		if td := p.Td(); bestPeer == nil || td.Cmp(bestTd) > 0 {
			bestPeer, bestTd = p, td
		}
	}
	return bestPeer
}

func (ps *peerSet) AllPeers() []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, len(ps.peers))
	i := 0
	for _, peer := range ps.peers {
		list[i] = peer
		i++
	}
	return list
}

func (ps *peerSet) Close() {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	for _, p := range ps.peers {
		p.Disconnect(p2p.DiscQuitting)
	}
	ps.closed = true
}
