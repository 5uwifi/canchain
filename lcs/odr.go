package lcs

import (
	"context"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/light"
)

type LesOdr struct {
	db                                         candb.Database
	indexerConfig                              *light.IndexerConfig
	chtIndexer, bloomTrieIndexer, bloomIndexer *kernel.ChainIndexer
	retriever                                  *retrieveManager
	stop                                       chan struct{}
}

func NewLesOdr(db candb.Database, config *light.IndexerConfig, retriever *retrieveManager) *LesOdr {
	return &LesOdr{
		db:            db,
		indexerConfig: config,
		retriever:     retriever,
		stop:          make(chan struct{}),
	}
}

func (odr *LesOdr) Stop() {
	close(odr.stop)
}

func (odr *LesOdr) Database() candb.Database {
	return odr.db
}

func (odr *LesOdr) SetIndexers(chtIndexer, bloomTrieIndexer, bloomIndexer *kernel.ChainIndexer) {
	odr.chtIndexer = chtIndexer
	odr.bloomTrieIndexer = bloomTrieIndexer
	odr.bloomIndexer = bloomIndexer
}

func (odr *LesOdr) ChtIndexer() *kernel.ChainIndexer {
	return odr.chtIndexer
}

func (odr *LesOdr) BloomTrieIndexer() *kernel.ChainIndexer {
	return odr.bloomTrieIndexer
}

func (odr *LesOdr) BloomIndexer() *kernel.ChainIndexer {
	return odr.bloomIndexer
}

func (odr *LesOdr) IndexerConfig() *light.IndexerConfig {
	return odr.indexerConfig
}

const (
	MsgBlockBodies = iota
	MsgCode
	MsgReceipts
	MsgProofsV1
	MsgProofsV2
	MsgHeaderProofs
	MsgHelperTrieProofs
)

type Msg struct {
	MsgType int
	ReqID   uint64
	Obj     interface{}
}

func (odr *LesOdr) Retrieve(ctx context.Context, req light.OdrRequest) (err error) {
	lreq := LesRequest(req)

	reqID := genReqID()
	rq := &distReq{
		getCost: func(dp distPeer) uint64 {
			return lreq.GetCost(dp.(*peer))
		},
		canSend: func(dp distPeer) bool {
			p := dp.(*peer)
			return lreq.CanSend(p)
		},
		request: func(dp distPeer) func() {
			p := dp.(*peer)
			cost := lreq.GetCost(p)
			p.fcServer.QueueRequest(reqID, cost)
			return func() { lreq.Request(reqID, p) }
		},
	}

	if err = odr.retriever.retrieve(ctx, reqID, rq, func(p distPeer, msg *Msg) error { return lreq.Validate(odr.db, msg) }, odr.stop); err == nil {
		req.StoreResult(odr.db)
	} else {
		log4j.Debug("Failed to retrieve data from network", "err", err)
	}
	return
}
