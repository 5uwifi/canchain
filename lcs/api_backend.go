package lcs

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
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/light"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/rpc"
)

type LcsApiBackend struct {
	can *LightCANChain
	gpo *gasprice.Oracle
}

func (b *LcsApiBackend) ChainConfig() *params.ChainConfig {
	return b.can.chainConfig
}

func (b *LcsApiBackend) CurrentBlock() *types.Block {
	return types.NewBlockWithHeader(b.can.BlockChain().CurrentHeader())
}

func (b *LcsApiBackend) SetHead(number uint64) {
	b.can.protocolManager.downloader.Cancel()
	b.can.blockchain.SetHead(number)
}

func (b *LcsApiBackend) HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error) {
	if blockNr == rpc.LatestBlockNumber || blockNr == rpc.PendingBlockNumber {
		return b.can.blockchain.CurrentHeader(), nil
	}
	return b.can.blockchain.GetHeaderByNumberOdr(ctx, uint64(blockNr))
}

func (b *LcsApiBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return b.can.blockchain.GetHeaderByHash(hash), nil
}

func (b *LcsApiBackend) BlockByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Block, error) {
	header, err := b.HeaderByNumber(ctx, blockNr)
	if header == nil || err != nil {
		return nil, err
	}
	return b.GetBlock(ctx, header.Hash())
}

func (b *LcsApiBackend) StateAndHeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*state.StateDB, *types.Header, error) {
	header, err := b.HeaderByNumber(ctx, blockNr)
	if header == nil || err != nil {
		return nil, nil, err
	}
	return light.NewState(ctx, header, b.can.odr), header, nil
}

func (b *LcsApiBackend) GetBlock(ctx context.Context, blockHash common.Hash) (*types.Block, error) {
	return b.can.blockchain.GetBlockByHash(ctx, blockHash)
}

func (b *LcsApiBackend) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	if number := rawdb.ReadHeaderNumber(b.can.chainDb, hash); number != nil {
		return light.GetBlockReceipts(ctx, b.can.odr, hash, *number)
	}
	return nil, nil
}

func (b *LcsApiBackend) GetLogs(ctx context.Context, hash common.Hash) ([][]*types.Log, error) {
	if number := rawdb.ReadHeaderNumber(b.can.chainDb, hash); number != nil {
		return light.GetBlockLogs(ctx, b.can.odr, hash, *number)
	}
	return nil, nil
}

func (b *LcsApiBackend) GetTd(hash common.Hash) *big.Int {
	return b.can.blockchain.GetTdByHash(hash)
}

func (b *LcsApiBackend) GetEVM(ctx context.Context, msg kernel.Message, state *state.StateDB, header *types.Header, vmCfg vm.Config) (*vm.EVM, func() error, error) {
	state.SetBalance(msg.From(), math.MaxBig256)
	context := kernel.NewEVMContext(msg, header, b.can.blockchain, nil)
	return vm.NewEVM(context, state, b.can.chainConfig, vmCfg), state.Error, nil
}

func (b *LcsApiBackend) SendTx(ctx context.Context, signedTx *types.Transaction) error {
	return b.can.txPool.Add(ctx, signedTx)
}

func (b *LcsApiBackend) RemoveTx(txHash common.Hash) {
	b.can.txPool.RemoveTx(txHash)
}

func (b *LcsApiBackend) GetPoolTransactions() (types.Transactions, error) {
	return b.can.txPool.GetTransactions()
}

func (b *LcsApiBackend) GetPoolTransaction(txHash common.Hash) *types.Transaction {
	return b.can.txPool.GetTransaction(txHash)
}

func (b *LcsApiBackend) GetPoolNonce(ctx context.Context, addr common.Address) (uint64, error) {
	return b.can.txPool.GetNonce(ctx, addr)
}

func (b *LcsApiBackend) Stats() (pending int, queued int) {
	return b.can.txPool.Stats(), 0
}

func (b *LcsApiBackend) TxPoolContent() (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	return b.can.txPool.Content()
}

func (b *LcsApiBackend) SubscribeNewTxsEvent(ch chan<- kernel.NewTxsEvent) event.Subscription {
	return b.can.txPool.SubscribeNewTxsEvent(ch)
}

func (b *LcsApiBackend) SubscribeChainEvent(ch chan<- kernel.ChainEvent) event.Subscription {
	return b.can.blockchain.SubscribeChainEvent(ch)
}

func (b *LcsApiBackend) SubscribeChainHeadEvent(ch chan<- kernel.ChainHeadEvent) event.Subscription {
	return b.can.blockchain.SubscribeChainHeadEvent(ch)
}

func (b *LcsApiBackend) SubscribeChainSideEvent(ch chan<- kernel.ChainSideEvent) event.Subscription {
	return b.can.blockchain.SubscribeChainSideEvent(ch)
}

func (b *LcsApiBackend) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return b.can.blockchain.SubscribeLogsEvent(ch)
}

func (b *LcsApiBackend) SubscribeRemovedLogsEvent(ch chan<- kernel.RemovedLogsEvent) event.Subscription {
	return b.can.blockchain.SubscribeRemovedLogsEvent(ch)
}

func (b *LcsApiBackend) Downloader() *downloader.Downloader {
	return b.can.Downloader()
}

func (b *LcsApiBackend) ProtocolVersion() int {
	return b.can.LcsVersion() + 10000
}

func (b *LcsApiBackend) SuggestPrice(ctx context.Context) (*big.Int, error) {
	return b.gpo.SuggestPrice(ctx)
}

func (b *LcsApiBackend) ChainDb() candb.Database {
	return b.can.chainDb
}

func (b *LcsApiBackend) EventMux() *event.TypeMux {
	return b.can.eventMux
}

func (b *LcsApiBackend) AccountManager() *accounts.Manager {
	return b.can.accountManager
}

func (b *LcsApiBackend) BloomStatus() (uint64, uint64) {
	if b.can.bloomIndexer == nil {
		return 0, 0
	}
	sections, _, _ := b.can.bloomIndexer.Sections()
	return params.BloomBitsBlocksClient, sections
}

func (b *LcsApiBackend) ServiceFilter(ctx context.Context, session *bloombits.MatcherSession) {
	for i := 0; i < bloomFilterThreads; i++ {
		go session.Multiplex(bloomRetrievalBatch, bloomRetrievalWait, b.can.bloomRequests)
	}
}
