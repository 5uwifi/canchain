package miner

import (
	"bytes"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/consensus/misc"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/params"
)

const (
	resultQueueSize   = 10
	txChanSize        = 4096
	chainHeadChanSize = 10
	chainSideChanSize = 10
	miningLogAtDepth  = 5
)

type Env struct {
	config *params.ChainConfig
	signer types.Signer

	state     *state.StateDB
	ancestors mapset.Set
	family    mapset.Set
	uncles    mapset.Set
	tcount    int
	gasPool   *kernel.GasPool

	header   *types.Header
	txs      []*types.Transaction
	receipts []*types.Receipt
}

func (env *Env) commitTransaction(tx *types.Transaction, bc *kernel.BlockChain, coinbase common.Address, gp *kernel.GasPool) (error, []*types.Log) {
	snap := env.state.Snapshot()

	receipt, _, err := kernel.ApplyTransaction(env.config, bc, &coinbase, gp, env.state, env.header, tx, &env.header.GasUsed, vm.Config{})
	if err != nil {
		env.state.RevertToSnapshot(snap)
		return err, nil
	}
	env.txs = append(env.txs, tx)
	env.receipts = append(env.receipts, receipt)

	return nil, receipt.Logs
}

func (env *Env) commitTransactions(mux *event.TypeMux, txs *types.TransactionsByPriceAndNonce, bc *kernel.BlockChain, coinbase common.Address) {
	if env.gasPool == nil {
		env.gasPool = new(kernel.GasPool).AddGas(env.header.GasLimit)
	}

	var coalescedLogs []*types.Log

	for {
		if env.gasPool.Gas() < params.TxGas {
			log4j.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}
		tx := txs.Peek()
		if tx == nil {
			break
		}
		from, _ := types.Sender(env.signer, tx)
		if tx.Protected() && !env.config.IsEIP155(env.header.Number) {
			log4j.Trace("Ignoring reply protected transaction", "hash", tx.Hash(), "eip155", env.config.EIP155Block)

			txs.Pop()
			continue
		}
		env.state.Prepare(tx.Hash(), common.Hash{}, env.tcount)

		err, logs := env.commitTransaction(tx, bc, coinbase, env.gasPool)
		switch err {
		case kernel.ErrGasLimitReached:
			log4j.Trace("Gas limit exceeded for current block", "sender", from)
			txs.Pop()

		case kernel.ErrNonceTooLow:
			log4j.Trace("Skipping transaction with low nonce", "sender", from, "nonce", tx.Nonce())
			txs.Shift()

		case kernel.ErrNonceTooHigh:
			log4j.Trace("Skipping account with hight nonce", "sender", from, "nonce", tx.Nonce())
			txs.Pop()

		case nil:
			coalescedLogs = append(coalescedLogs, logs...)
			env.tcount++
			txs.Shift()

		default:
			log4j.Debug("Transaction failed, account skipped", "hash", tx.Hash(), "err", err)
			txs.Shift()
		}
	}

	if len(coalescedLogs) > 0 || env.tcount > 0 {
		cpy := make([]*types.Log, len(coalescedLogs))
		for i, l := range coalescedLogs {
			cpy[i] = new(types.Log)
			*cpy[i] = *l
		}
		go func(logs []*types.Log, tcount int) {
			if len(logs) > 0 {
				mux.Post(kernel.PendingLogsEvent{Logs: logs})
			}
			if tcount > 0 {
				mux.Post(kernel.PendingStateEvent{})
			}
		}(cpy, env.tcount)
	}
}

type task struct {
	receipts  []*types.Receipt
	state     *state.StateDB
	block     *types.Block
	createdAt time.Time
}

type worker struct {
	config *params.ChainConfig
	engine consensus.Engine
	eth    Backend
	chain  *kernel.BlockChain

	mux          *event.TypeMux
	txsCh        chan kernel.NewTxsEvent
	txsSub       event.Subscription
	chainHeadCh  chan kernel.ChainHeadEvent
	chainHeadSub event.Subscription
	chainSideCh  chan kernel.ChainSideEvent
	chainSideSub event.Subscription

	newWork  chan struct{}
	taskCh   chan *task
	resultCh chan *task
	exitCh   chan struct{}

	current        *Env
	possibleUncles map[common.Hash]*types.Block
	unconfirmed    *unconfirmedBlocks

	mu       sync.RWMutex
	coinbase common.Address
	extra    []byte

	snapshotMu    sync.RWMutex
	snapshotBlock *types.Block
	snapshotState *state.StateDB

	running int32

	newTaskHook      func(*task)
	fullTaskInterval func()
}

func newWorker(config *params.ChainConfig, engine consensus.Engine, eth Backend, mux *event.TypeMux) *worker {
	worker := &worker{
		config:         config,
		engine:         engine,
		eth:            eth,
		mux:            mux,
		chain:          eth.BlockChain(),
		possibleUncles: make(map[common.Hash]*types.Block),
		unconfirmed:    newUnconfirmedBlocks(eth.BlockChain(), miningLogAtDepth),
		txsCh:          make(chan kernel.NewTxsEvent, txChanSize),
		chainHeadCh:    make(chan kernel.ChainHeadEvent, chainHeadChanSize),
		chainSideCh:    make(chan kernel.ChainSideEvent, chainSideChanSize),
		newWork:        make(chan struct{}, 1),
		taskCh:         make(chan *task),
		resultCh:       make(chan *task, resultQueueSize),
		exitCh:         make(chan struct{}),
	}
	worker.txsSub = eth.TxPool().SubscribeNewTxsEvent(worker.txsCh)
	worker.chainHeadSub = eth.BlockChain().SubscribeChainHeadEvent(worker.chainHeadCh)
	worker.chainSideSub = eth.BlockChain().SubscribeChainSideEvent(worker.chainSideCh)

	go worker.mainLoop()
	go worker.resultLoop()
	go worker.taskLoop()

	worker.newWork <- struct{}{}
	return worker
}

func (w *worker) setCanerbase(addr common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

func (w *worker) setExtra(extra []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extra = extra
}

func (w *worker) pending() (*types.Block, *state.StateDB) {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	if w.snapshotState == nil {
		return nil, nil
	}
	return w.snapshotBlock, w.snapshotState.Copy()
}

func (w *worker) pendingBlock() *types.Block {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock
}

func (w *worker) start() {
	atomic.StoreInt32(&w.running, 1)
	w.newWork <- struct{}{}
}

func (w *worker) stop() {
	atomic.StoreInt32(&w.running, 0)
}

func (w *worker) isRunning() bool {
	return atomic.LoadInt32(&w.running) == 1
}

func (w *worker) close() {
	close(w.exitCh)
	for empty := false; !empty; {
		select {
		case <-w.resultCh:
		default:
			empty = true
		}
	}
}

func (w *worker) mainLoop() {
	defer w.txsSub.Unsubscribe()
	defer w.chainHeadSub.Unsubscribe()
	defer w.chainSideSub.Unsubscribe()

	for {
		select {
		case <-w.newWork:
			w.commitNewWork()

		case <-w.chainHeadCh:
			w.commitNewWork()

		case ev := <-w.chainSideCh:
			w.possibleUncles[ev.Block.Hash()] = ev.Block

		case ev := <-w.txsCh:
			if !w.isRunning() && w.current != nil {
				w.mu.Lock()
				coinbase := w.coinbase
				w.mu.Unlock()

				txs := make(map[common.Address]types.Transactions)
				for _, tx := range ev.Txs {
					acc, _ := types.Sender(w.current.signer, tx)
					txs[acc] = append(txs[acc], tx)
				}
				txset := types.NewTransactionsByPriceAndNonce(w.current.signer, txs)
				w.current.commitTransactions(w.mux, txset, w.chain, coinbase)
				w.updateSnapshot()
			} else {
				if w.config.Clique != nil && w.config.Clique.Period == 0 {
					w.commitNewWork()
				}
			}

		case <-w.exitCh:
			return
		case <-w.txsSub.Err():
			return
		case <-w.chainHeadSub.Err():
			return
		case <-w.chainSideSub.Err():
			return
		}
	}
}

func (w *worker) seal(t *task, stop <-chan struct{}) {
	var (
		err error
		res *task
	)

	if t.block, err = w.engine.Seal(w.chain, t.block, stop); t.block != nil {
		log4j.Info("Successfully sealed new block", "number", t.block.Number(), "hash", t.block.Hash(),
			"elapsed", common.PrettyDuration(time.Since(t.createdAt)))
		res = t
	} else {
		if err != nil {
			log4j.Warn("Block sealing failed", "err", err)
		}
		res = nil
	}
	select {
	case w.resultCh <- res:
	case <-w.exitCh:
	}
}

func (w *worker) taskLoop() {
	var stopCh chan struct{}

	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}
	for {
		select {
		case task := <-w.taskCh:
			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}
			interrupt()
			stopCh = make(chan struct{})
			go w.seal(task, stopCh)
		case <-w.exitCh:
			interrupt()
			return
		}
	}
}

func (w *worker) resultLoop() {
	for {
		select {
		case result := <-w.resultCh:
			if result == nil {
				continue
			}
			block := result.block

			for _, r := range result.receipts {
				for _, l := range r.Logs {
					l.BlockHash = block.Hash()
				}
			}
			for _, log := range result.state.Logs() {
				log.BlockHash = block.Hash()
			}
			stat, err := w.chain.WriteBlockWithState(block, result.receipts, result.state)
			if err != nil {
				log4j.Error("Failed writing block to chain", "err", err)
				continue
			}
			w.mux.Post(kernel.NewMinedBlockEvent{Block: block})
			var (
				events []interface{}
				logs   = result.state.Logs()
			)
			switch stat {
			case kernel.CanonStatTy:
				events = append(events, kernel.ChainEvent{Block: block, Hash: block.Hash(), Logs: logs})
				events = append(events, kernel.ChainHeadEvent{Block: block})
			case kernel.SideStatTy:
				events = append(events, kernel.ChainSideEvent{Block: block})
			}
			w.chain.PostChainEvents(events, logs)

			w.unconfirmed.Insert(block.NumberU64(), block.Hash())

		case <-w.exitCh:
			return
		}
	}
}

func (w *worker) makeCurrent(parent *types.Block, header *types.Header) error {
	state, err := w.chain.StateAt(parent.Root())
	if err != nil {
		return err
	}
	env := &Env{
		config:    w.config,
		signer:    types.NewEIP155Signer(w.config.ChainID),
		state:     state,
		ancestors: mapset.NewSet(),
		family:    mapset.NewSet(),
		uncles:    mapset.NewSet(),
		header:    header,
	}

	for _, ancestor := range w.chain.GetBlocksFromHash(parent.Hash(), 7) {
		for _, uncle := range ancestor.Uncles() {
			env.family.Add(uncle.Hash())
		}
		env.family.Add(ancestor.Hash())
		env.ancestors.Add(ancestor.Hash())
	}

	env.tcount = 0
	w.current = env
	return nil
}

func (w *worker) commitUncle(env *Env, uncle *types.Header) error {
	hash := uncle.Hash()
	if env.uncles.Contains(hash) {
		return fmt.Errorf("uncle not unique")
	}
	if !env.ancestors.Contains(uncle.ParentHash) {
		return fmt.Errorf("uncle's parent unknown (%x)", uncle.ParentHash[0:4])
	}
	if env.family.Contains(hash) {
		return fmt.Errorf("uncle already in family (%x)", hash)
	}
	env.uncles.Add(uncle.Hash())
	return nil
}

func (w *worker) updateSnapshot() {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	var uncles []*types.Header
	w.current.uncles.Each(func(item interface{}) bool {
		hash, ok := item.(common.Hash)
		if !ok {
			return false
		}
		uncle, exist := w.possibleUncles[hash]
		if !exist {
			return false
		}
		uncles = append(uncles, uncle.Header())
		return true
	})

	w.snapshotBlock = types.NewBlock(
		w.current.header,
		w.current.txs,
		uncles,
		w.current.receipts,
	)

	w.snapshotState = w.current.state.Copy()
}

func (w *worker) commitNewWork() {
	w.mu.RLock()
	defer w.mu.RUnlock()

	tstart := time.Now()
	parent := w.chain.CurrentBlock()

	tstamp := tstart.Unix()
	if parent.Time().Cmp(new(big.Int).SetInt64(tstamp)) >= 0 {
		tstamp = parent.Time().Int64() + 1
	}
	if now := time.Now().Unix(); tstamp > now+1 {
		wait := time.Duration(tstamp-now) * time.Second
		log4j.Info("Mining too far in the future", "wait", common.PrettyDuration(wait))
		time.Sleep(wait)
	}

	num := parent.Number()
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     num.Add(num, common.Big1),
		GasLimit:   kernel.CalcGasLimit(parent),
		Extra:      w.extra,
		Time:       big.NewInt(tstamp),
	}
	if w.isRunning() {
		if w.coinbase == (common.Address{}) {
			log4j.Error("Refusing to mine without canerbase")
			return
		}
		header.Coinbase = w.coinbase
	}
	if err := w.engine.Prepare(w.chain, header); err != nil {
		log4j.Error("Failed to prepare header for mining", "err", err)
		return
	}
	if daoBlock := w.config.DAOForkBlock; daoBlock != nil {
		limit := new(big.Int).Add(daoBlock, params.DAOForkExtraRange)
		if header.Number.Cmp(daoBlock) >= 0 && header.Number.Cmp(limit) < 0 {
			if w.config.DAOForkSupport {
				header.Extra = common.CopyBytes(params.DAOForkBlockExtra)
			} else if bytes.Equal(header.Extra, params.DAOForkBlockExtra) {
				header.Extra = []byte{}
			}
		}
	}
	err := w.makeCurrent(parent, header)
	if err != nil {
		log4j.Error("Failed to create mining context", "err", err)
		return
	}
	env := w.current
	if w.config.DAOForkSupport && w.config.DAOForkBlock != nil && w.config.DAOForkBlock.Cmp(header.Number) == 0 {
		misc.ApplyDAOHardFork(env.state)
	}

	var (
		uncles    []*types.Header
		badUncles []common.Hash
	)
	for hash, uncle := range w.possibleUncles {
		if len(uncles) == 2 {
			break
		}
		if err := w.commitUncle(env, uncle.Header()); err != nil {
			log4j.Trace("Bad uncle found and will be removed", "hash", hash)
			log4j.Trace(fmt.Sprint(uncle))

			badUncles = append(badUncles, hash)
		} else {
			log4j.Debug("Committing new uncle to block", "hash", hash)
			uncles = append(uncles, uncle.Header())
		}
	}
	for _, hash := range badUncles {
		delete(w.possibleUncles, hash)
	}

	var (
		emptyBlock, fullBlock *types.Block
		emptyState, fullState *state.StateDB
	)

	emptyState = env.state.Copy()
	if emptyBlock, err = w.engine.Finalize(w.chain, header, emptyState, nil, uncles, nil); err != nil {
		log4j.Error("Failed to finalize block for temporary sealing", "err", err)
	} else {
		if w.isRunning() {
			select {
			case w.taskCh <- &task{receipts: nil, state: emptyState, block: emptyBlock, createdAt: time.Now()}:
				log4j.Info("Commit new empty mining work", "number", emptyBlock.Number(), "uncles", len(uncles))
			case <-w.exitCh:
				log4j.Info("Worker has exited")
				return
			}
		}
	}

	pending, err := w.eth.TxPool().Pending()
	if err != nil {
		log4j.Error("Failed to fetch pending transactions", "err", err)
		return
	}
	if len(pending) == 0 {
		w.updateSnapshot()
		return
	}
	txs := types.NewTransactionsByPriceAndNonce(w.current.signer, pending)
	env.commitTransactions(w.mux, txs, w.chain, w.coinbase)

	fullState = env.state.Copy()
	if fullBlock, err = w.engine.Finalize(w.chain, header, fullState, env.txs, uncles, env.receipts); err != nil {
		log4j.Error("Failed to finalize block for sealing", "err", err)
		return
	}
	cpy := make([]*types.Receipt, len(env.receipts))
	for i, l := range env.receipts {
		cpy[i] = new(types.Receipt)
		*cpy[i] = *l
	}
	if w.isRunning() {
		if w.fullTaskInterval != nil {
			w.fullTaskInterval()
		}

		select {
		case w.taskCh <- &task{receipts: cpy, state: fullState, block: fullBlock, createdAt: time.Now()}:
			w.unconfirmed.Shift(fullBlock.NumberU64() - 1)
			log4j.Info("Commit new full mining work", "number", fullBlock.Number(), "txs", env.tcount, "uncles", len(uncles), "elapsed", common.PrettyDuration(time.Since(tstart)))
		case <-w.exitCh:
			log4j.Info("Worker has exited")
		}
	}
	w.updateSnapshot()
}
