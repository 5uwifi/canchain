package can

import (
	"context"
	"math/big"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/can/gasprice"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/math"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/bloombits"
	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/rpc"
)

type CanAPIBackend struct {
	can *CANChain
	gpo *gasprice.Oracle
}

func (b *CanAPIBackend) ChainConfig() *params.ChainConfig {
	return b.can.chainConfig
}

func (b *CanAPIBackend) CurrentBlock() *types.Block {
	return b.can.blockchain.CurrentBlock()
}

func (b *CanAPIBackend) SetHead(number uint64) {
	b.can.protocolManager.downloader.Cancel()
	b.can.blockchain.SetHead(number)
}

func (b *CanAPIBackend) HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error) {
	if blockNr == rpc.PendingBlockNumber {
		block := b.can.miner.PendingBlock()
		return block.Header(), nil
	}
	if blockNr == rpc.LatestBlockNumber {
		return b.can.blockchain.CurrentBlock().Header(), nil
	}
	return b.can.blockchain.GetHeaderByNumber(uint64(blockNr)), nil
}

func (b *CanAPIBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return b.can.blockchain.GetHeaderByHash(hash), nil
}

func (b *CanAPIBackend) BlockByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Block, error) {
	if blockNr == rpc.PendingBlockNumber {
		block := b.can.miner.PendingBlock()
		return block, nil
	}
	if blockNr == rpc.LatestBlockNumber {
		return b.can.blockchain.CurrentBlock(), nil
	}
	return b.can.blockchain.GetBlockByNumber(uint64(blockNr)), nil
}

func (b *CanAPIBackend) StateAndHeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*state.StateDB, *types.Header, error) {
	if blockNr == rpc.PendingBlockNumber {
		block, state := b.can.miner.Pending()
		return state, block.Header(), nil
	}
	header, err := b.HeaderByNumber(ctx, blockNr)
	if header == nil || err != nil {
		return nil, nil, err
	}
	stateDb, err := b.can.BlockChain().StateAt(header.Root)
	return stateDb, header, err
}

func (b *CanAPIBackend) GetBlock(ctx context.Context, hash common.Hash) (*types.Block, error) {
	return b.can.blockchain.GetBlockByHash(hash), nil
}

func (b *CanAPIBackend) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	return b.can.blockchain.GetReceiptsByHash(hash), nil
}

func (b *CanAPIBackend) GetLogs(ctx context.Context, hash common.Hash) ([][]*types.Log, error) {
	receipts := b.can.blockchain.GetReceiptsByHash(hash)
	if receipts == nil {
		return nil, nil
	}
	logs := make([][]*types.Log, len(receipts))
	for i, receipt := range receipts {
		logs[i] = receipt.Logs
	}
	return logs, nil
}

func (b *CanAPIBackend) GetTd(blockHash common.Hash) *big.Int {
	return b.can.blockchain.GetTdByHash(blockHash)
}

func (b *CanAPIBackend) GetEVM(ctx context.Context, msg kernel.Message, state *state.StateDB, header *types.Header, vmCfg vm.Config) (*vm.EVM, func() error, error) {
	state.SetBalance(msg.From(), math.MaxBig256)
	vmError := func() error { return nil }

	context := kernel.NewEVMContext(msg, header, b.can.BlockChain(), nil)
	return vm.NewEVM(context, state, b.can.chainConfig, vmCfg), vmError, nil
}

func (b *CanAPIBackend) SubscribeRemovedLogsEvent(ch chan<- kernel.RemovedLogsEvent) event.Subscription {
	return b.can.BlockChain().SubscribeRemovedLogsEvent(ch)
}

func (b *CanAPIBackend) SubscribeChainEvent(ch chan<- kernel.ChainEvent) event.Subscription {
	return b.can.BlockChain().SubscribeChainEvent(ch)
}

func (b *CanAPIBackend) SubscribeChainHeadEvent(ch chan<- kernel.ChainHeadEvent) event.Subscription {
	return b.can.BlockChain().SubscribeChainHeadEvent(ch)
}

func (b *CanAPIBackend) SubscribeChainSideEvent(ch chan<- kernel.ChainSideEvent) event.Subscription {
	return b.can.BlockChain().SubscribeChainSideEvent(ch)
}

func (b *CanAPIBackend) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return b.can.BlockChain().SubscribeLogsEvent(ch)
}

func (b *CanAPIBackend) SendTx(ctx context.Context, signedTx *types.Transaction) error {
	return b.can.txPool.AddLocal(signedTx)
}

func (b *CanAPIBackend) GetPoolTransactions() (types.Transactions, error) {
	pending, err := b.can.txPool.Pending()
	if err != nil {
		return nil, err
	}
	var txs types.Transactions
	for _, batch := range pending {
		txs = append(txs, batch...)
	}
	return txs, nil
}

func (b *CanAPIBackend) GetPoolTransaction(hash common.Hash) *types.Transaction {
	return b.can.txPool.Get(hash)
}

func (b *CanAPIBackend) GetPoolNonce(ctx context.Context, addr common.Address) (uint64, error) {
	return b.can.txPool.State().GetNonce(addr), nil
}

func (b *CanAPIBackend) Stats() (pending int, queued int) {
	return b.can.txPool.Stats()
}

func (b *CanAPIBackend) TxPoolContent() (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	return b.can.TxPool().Content()
}

func (b *CanAPIBackend) SubscribeNewTxsEvent(ch chan<- kernel.NewTxsEvent) event.Subscription {
	return b.can.TxPool().SubscribeNewTxsEvent(ch)
}

func (b *CanAPIBackend) Downloader() *downloader.Downloader {
	return b.can.Downloader()
}

func (b *CanAPIBackend) ProtocolVersion() int {
	return b.can.EthVersion()
}

func (b *CanAPIBackend) SuggestPrice(ctx context.Context) (*big.Int, error) {
	return b.gpo.SuggestPrice(ctx)
}

func (b *CanAPIBackend) ChainDb() candb.Database {
	return b.can.ChainDb()
}

func (b *CanAPIBackend) EventMux() *event.TypeMux {
	return b.can.EventMux()
}

func (b *CanAPIBackend) AccountManager() *accounts.Manager {
	return b.can.AccountManager()
}

func (b *CanAPIBackend) BloomStatus() (uint64, uint64) {
	sections, _, _ := b.can.bloomIndexer.Sections()
	return params.BloomBitsBlocks, sections
}

func (b *CanAPIBackend) ServiceFilter(ctx context.Context, session *bloombits.MatcherSession) {
	for i := 0; i < bloomFilterThreads; i++ {
		go session.Multiplex(bloomRetrievalBatch, bloomRetrievalWait, b.can.bloomRequests)
	}
}
