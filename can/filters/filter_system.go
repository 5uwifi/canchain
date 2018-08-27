package filters

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/rpc"
)

type Type byte

const (
	UnknownSubscription Type = iota
	LogsSubscription
	PendingLogsSubscription
	MinedAndPendingLogsSubscription
	PendingTransactionsSubscription
	BlocksSubscription
	LastIndexSubscription
)

const (
	txChanSize      = 4096
	rmLogsChanSize  = 10
	logsChanSize    = 10
	chainEvChanSize = 10
)

var (
	ErrInvalidSubscriptionID = errors.New("invalid id")
)

type subscription struct {
	id        rpc.ID
	typ       Type
	created   time.Time
	logsCrit  canchain.FilterQuery
	logs      chan []*types.Log
	hashes    chan []common.Hash
	headers   chan *types.Header
	installed chan struct{}
	err       chan error
}

type EventSystem struct {
	mux       *event.TypeMux
	backend   Backend
	lightMode bool
	lastHead  *types.Header

	txsSub        event.Subscription
	logsSub       event.Subscription
	rmLogsSub     event.Subscription
	chainSub      event.Subscription
	pendingLogSub *event.TypeMuxSubscription

	install   chan *subscription
	uninstall chan *subscription
	txsCh     chan kernel.NewTxsEvent
	logsCh    chan []*types.Log
	rmLogsCh  chan kernel.RemovedLogsEvent
	chainCh   chan kernel.ChainEvent
}

func NewEventSystem(mux *event.TypeMux, backend Backend, lightMode bool) *EventSystem {
	m := &EventSystem{
		mux:       mux,
		backend:   backend,
		lightMode: lightMode,
		install:   make(chan *subscription),
		uninstall: make(chan *subscription),
		txsCh:     make(chan kernel.NewTxsEvent, txChanSize),
		logsCh:    make(chan []*types.Log, logsChanSize),
		rmLogsCh:  make(chan kernel.RemovedLogsEvent, rmLogsChanSize),
		chainCh:   make(chan kernel.ChainEvent, chainEvChanSize),
	}

	m.txsSub = m.backend.SubscribeNewTxsEvent(m.txsCh)
	m.logsSub = m.backend.SubscribeLogsEvent(m.logsCh)
	m.rmLogsSub = m.backend.SubscribeRemovedLogsEvent(m.rmLogsCh)
	m.chainSub = m.backend.SubscribeChainEvent(m.chainCh)
	m.pendingLogSub = m.mux.Subscribe(kernel.PendingLogsEvent{})

	if m.txsSub == nil || m.logsSub == nil || m.rmLogsSub == nil || m.chainSub == nil ||
		m.pendingLogSub.Closed() {
		log4j.Crit("Subscribe for event system failed")
	}

	go m.eventLoop()
	return m
}

type Subscription struct {
	ID        rpc.ID
	f         *subscription
	es        *EventSystem
	unsubOnce sync.Once
}

func (sub *Subscription) Err() <-chan error {
	return sub.f.err
}

func (sub *Subscription) Unsubscribe() {
	sub.unsubOnce.Do(func() {
	uninstallLoop:
		for {
			select {
			case sub.es.uninstall <- sub.f:
				break uninstallLoop
			case <-sub.f.logs:
			case <-sub.f.hashes:
			case <-sub.f.headers:
			}
		}

		<-sub.Err()
	})
}

func (es *EventSystem) subscribe(sub *subscription) *Subscription {
	es.install <- sub
	<-sub.installed
	return &Subscription{ID: sub.id, f: sub, es: es}
}

func (es *EventSystem) SubscribeLogs(crit canchain.FilterQuery, logs chan []*types.Log) (*Subscription, error) {
	var from, to rpc.BlockNumber
	if crit.FromBlock == nil {
		from = rpc.LatestBlockNumber
	} else {
		from = rpc.BlockNumber(crit.FromBlock.Int64())
	}
	if crit.ToBlock == nil {
		to = rpc.LatestBlockNumber
	} else {
		to = rpc.BlockNumber(crit.ToBlock.Int64())
	}

	if from == rpc.PendingBlockNumber && to == rpc.PendingBlockNumber {
		return es.subscribePendingLogs(crit, logs), nil
	}
	if from == rpc.LatestBlockNumber && to == rpc.LatestBlockNumber {
		return es.subscribeLogs(crit, logs), nil
	}
	if from >= 0 && to >= 0 && to >= from {
		return es.subscribeLogs(crit, logs), nil
	}
	if from >= rpc.LatestBlockNumber && to == rpc.PendingBlockNumber {
		return es.subscribeMinedPendingLogs(crit, logs), nil
	}
	if from >= 0 && to == rpc.LatestBlockNumber {
		return es.subscribeLogs(crit, logs), nil
	}
	return nil, fmt.Errorf("invalid from and to block combination: from > to")
}

func (es *EventSystem) subscribeMinedPendingLogs(crit canchain.FilterQuery, logs chan []*types.Log) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       MinedAndPendingLogsSubscription,
		logsCrit:  crit,
		created:   time.Now(),
		logs:      logs,
		hashes:    make(chan []common.Hash),
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

func (es *EventSystem) subscribeLogs(crit canchain.FilterQuery, logs chan []*types.Log) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       LogsSubscription,
		logsCrit:  crit,
		created:   time.Now(),
		logs:      logs,
		hashes:    make(chan []common.Hash),
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

func (es *EventSystem) subscribePendingLogs(crit canchain.FilterQuery, logs chan []*types.Log) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       PendingLogsSubscription,
		logsCrit:  crit,
		created:   time.Now(),
		logs:      logs,
		hashes:    make(chan []common.Hash),
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

func (es *EventSystem) SubscribeNewHeads(headers chan *types.Header) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       BlocksSubscription,
		created:   time.Now(),
		logs:      make(chan []*types.Log),
		hashes:    make(chan []common.Hash),
		headers:   headers,
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

func (es *EventSystem) SubscribePendingTxs(hashes chan []common.Hash) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       PendingTransactionsSubscription,
		created:   time.Now(),
		logs:      make(chan []*types.Log),
		hashes:    hashes,
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

type filterIndex map[Type]map[rpc.ID]*subscription

func (es *EventSystem) broadcast(filters filterIndex, ev interface{}) {
	if ev == nil {
		return
	}

	switch e := ev.(type) {
	case []*types.Log:
		if len(e) > 0 {
			for _, f := range filters[LogsSubscription] {
				if matchedLogs := filterLogs(e, f.logsCrit.FromBlock, f.logsCrit.ToBlock, f.logsCrit.Addresses, f.logsCrit.Topics); len(matchedLogs) > 0 {
					f.logs <- matchedLogs
				}
			}
		}
	case kernel.RemovedLogsEvent:
		for _, f := range filters[LogsSubscription] {
			if matchedLogs := filterLogs(e.Logs, f.logsCrit.FromBlock, f.logsCrit.ToBlock, f.logsCrit.Addresses, f.logsCrit.Topics); len(matchedLogs) > 0 {
				f.logs <- matchedLogs
			}
		}
	case *event.TypeMuxEvent:
		if muxe, ok := e.Data.(kernel.PendingLogsEvent); ok {
			for _, f := range filters[PendingLogsSubscription] {
				if e.Time.After(f.created) {
					if matchedLogs := filterLogs(muxe.Logs, nil, f.logsCrit.ToBlock, f.logsCrit.Addresses, f.logsCrit.Topics); len(matchedLogs) > 0 {
						f.logs <- matchedLogs
					}
				}
			}
		}
	case kernel.NewTxsEvent:
		hashes := make([]common.Hash, 0, len(e.Txs))
		for _, tx := range e.Txs {
			hashes = append(hashes, tx.Hash())
		}
		for _, f := range filters[PendingTransactionsSubscription] {
			f.hashes <- hashes
		}
	case kernel.ChainEvent:
		for _, f := range filters[BlocksSubscription] {
			f.headers <- e.Block.Header()
		}
		if es.lightMode && len(filters[LogsSubscription]) > 0 {
			es.lightFilterNewHead(e.Block.Header(), func(header *types.Header, remove bool) {
				for _, f := range filters[LogsSubscription] {
					if matchedLogs := es.lightFilterLogs(header, f.logsCrit.Addresses, f.logsCrit.Topics, remove); len(matchedLogs) > 0 {
						f.logs <- matchedLogs
					}
				}
			})
		}
	}
}

func (es *EventSystem) lightFilterNewHead(newHeader *types.Header, callBack func(*types.Header, bool)) {
	oldh := es.lastHead
	es.lastHead = newHeader
	if oldh == nil {
		return
	}
	newh := newHeader
	var oldHeaders, newHeaders []*types.Header
	for oldh.Hash() != newh.Hash() {
		if oldh.Number.Uint64() >= newh.Number.Uint64() {
			oldHeaders = append(oldHeaders, oldh)
			oldh = rawdb.ReadHeader(es.backend.ChainDb(), oldh.ParentHash, oldh.Number.Uint64()-1)
		}
		if oldh.Number.Uint64() < newh.Number.Uint64() {
			newHeaders = append(newHeaders, newh)
			newh = rawdb.ReadHeader(es.backend.ChainDb(), newh.ParentHash, newh.Number.Uint64()-1)
			if newh == nil {
				newh = oldh
			}
		}
	}
	for _, h := range oldHeaders {
		callBack(h, true)
	}
	for i := len(newHeaders) - 1; i >= 0; i-- {
		callBack(newHeaders[i], false)
	}
}

func (es *EventSystem) lightFilterLogs(header *types.Header, addresses []common.Address, topics [][]common.Hash, remove bool) []*types.Log {
	if bloomFilter(header.Bloom, addresses, topics) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		logsList, err := es.backend.GetLogs(ctx, header.Hash())
		if err != nil {
			return nil
		}
		var unfiltered []*types.Log
		for _, logs := range logsList {
			for _, log := range logs {
				logcopy := *log
				logcopy.Removed = remove
				unfiltered = append(unfiltered, &logcopy)
			}
		}
		logs := filterLogs(unfiltered, nil, nil, addresses, topics)
		if len(logs) > 0 && logs[0].TxHash == (common.Hash{}) {
			receipts, err := es.backend.GetReceipts(ctx, header.Hash())
			if err != nil {
				return nil
			}
			unfiltered = unfiltered[:0]
			for _, receipt := range receipts {
				for _, log := range receipt.Logs {
					logcopy := *log
					logcopy.Removed = remove
					unfiltered = append(unfiltered, &logcopy)
				}
			}
			logs = filterLogs(unfiltered, nil, nil, addresses, topics)
		}
		return logs
	}
	return nil
}

func (es *EventSystem) eventLoop() {
	defer func() {
		es.pendingLogSub.Unsubscribe()
		es.txsSub.Unsubscribe()
		es.logsSub.Unsubscribe()
		es.rmLogsSub.Unsubscribe()
		es.chainSub.Unsubscribe()
	}()

	index := make(filterIndex)
	for i := UnknownSubscription; i < LastIndexSubscription; i++ {
		index[i] = make(map[rpc.ID]*subscription)
	}

	for {
		select {
		case ev := <-es.txsCh:
			es.broadcast(index, ev)
		case ev := <-es.logsCh:
			es.broadcast(index, ev)
		case ev := <-es.rmLogsCh:
			es.broadcast(index, ev)
		case ev := <-es.chainCh:
			es.broadcast(index, ev)
		case ev, active := <-es.pendingLogSub.Chan():
			if !active {
				return
			}
			es.broadcast(index, ev)

		case f := <-es.install:
			if f.typ == MinedAndPendingLogsSubscription {
				index[LogsSubscription][f.id] = f
				index[PendingLogsSubscription][f.id] = f
			} else {
				index[f.typ][f.id] = f
			}
			close(f.installed)

		case f := <-es.uninstall:
			if f.typ == MinedAndPendingLogsSubscription {
				delete(index[LogsSubscription], f.id)
				delete(index[PendingLogsSubscription], f.id)
			} else {
				delete(index[f.typ], f.id)
			}
			close(f.err)

		case <-es.txsSub.Err():
			return
		case <-es.logsSub.Err():
			return
		case <-es.rmLogsSub.Err():
			return
		case <-es.chainSub.Err():
			return
		}
	}
}
