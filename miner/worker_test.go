package miner

import (
	"math/big"
	"testing"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/consensus/clique"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/params"
)

var (
	testTxPoolConfig  kernel.TxPoolConfig
	ethashChainConfig *params.ChainConfig
	cliqueChainConfig *params.ChainConfig

	testBankKey, _  = crypto.GenerateKey()
	testBankAddress = crypto.PubkeyToAddress(testBankKey.PublicKey)
	testBankFunds   = big.NewInt(1000000000000000000)

	acc1Key, _ = crypto.GenerateKey()
	acc1Addr   = crypto.PubkeyToAddress(acc1Key.PublicKey)

	pendingTxs []*types.Transaction
	newTxs     []*types.Transaction
)

func init() {
	testTxPoolConfig = kernel.DefaultTxPoolConfig
	testTxPoolConfig.Journal = ""
	ethashChainConfig = params.TestChainConfig
	cliqueChainConfig = params.TestChainConfig
	cliqueChainConfig.Clique = &params.CliqueConfig{
		Period: 1,
		Epoch:  30000,
	}
	tx1, _ := types.SignTx(types.NewTransaction(0, acc1Addr, big.NewInt(1000), params.TxGas, nil, nil), types.HomesteadSigner{}, testBankKey)
	pendingTxs = append(pendingTxs, tx1)
	tx2, _ := types.SignTx(types.NewTransaction(1, acc1Addr, big.NewInt(1000), params.TxGas, nil, nil), types.HomesteadSigner{}, testBankKey)
	newTxs = append(newTxs, tx2)
}

type testWorkerBackend struct {
	db         candb.Database
	txPool     *kernel.TxPool
	chain      *kernel.BlockChain
	testTxFeed event.Feed
}

func newTestWorkerBackend(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) *testWorkerBackend {
	var (
		db    = candb.NewMemDatabase()
		gspec = kernel.Genesis{
			Config: chainConfig,
			Alloc:  kernel.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
		}
	)

	switch engine.(type) {
	case *clique.Clique:
		gspec.ExtraData = make([]byte, 32+common.AddressLength+65)
		copy(gspec.ExtraData[32:], testBankAddress[:])
	case *ethash.Ethash:
	default:
		t.Fatal("unexpect consensus engine type")
	}
	gspec.MustCommit(db)

	chain, _ := kernel.NewBlockChain(db, nil, gspec.Config, engine, vm.Config{})
	txpool := kernel.NewTxPool(testTxPoolConfig, chainConfig, chain)

	return &testWorkerBackend{
		db:     db,
		chain:  chain,
		txPool: txpool,
	}
}

func (b *testWorkerBackend) BlockChain() *kernel.BlockChain { return b.chain }
func (b *testWorkerBackend) TxPool() *kernel.TxPool         { return b.txPool }
func (b *testWorkerBackend) PostChainEvents(events []interface{}) {
	b.chain.PostChainEvents(events, nil)
}

func newTestWorker(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) (*worker, *testWorkerBackend) {
	backend := newTestWorkerBackend(t, chainConfig, engine)
	backend.txPool.AddLocals(pendingTxs)
	w := newWorker(chainConfig, engine, backend, new(event.TypeMux))
	w.setCanerbase(testBankAddress)
	return w, backend
}

func TestPendingStateAndBlockEthash(t *testing.T) {
	testPendingStateAndBlock(t, ethashChainConfig, ethash.NewFaker())
}
func TestPendingStateAndBlockClique(t *testing.T) {
	testPendingStateAndBlock(t, cliqueChainConfig, clique.New(cliqueChainConfig.Clique, candb.NewMemDatabase()))
}

func testPendingStateAndBlock(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	defer engine.Close()

	w, b := newTestWorker(t, chainConfig, engine)
	defer w.close()

	time.Sleep(100 * time.Millisecond)
	block, state := w.pending()
	if block.NumberU64() != 1 {
		t.Errorf("block number mismatch, has %d, want %d", block.NumberU64(), 1)
	}
	if balance := state.GetBalance(acc1Addr); balance.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("account balance mismatch, has %d, want %d", balance, 1000)
	}
	b.txPool.AddLocals(newTxs)
	time.Sleep(100 * time.Millisecond)
	block, state = w.pending()
	if balance := state.GetBalance(acc1Addr); balance.Cmp(big.NewInt(2000)) != 0 {
		t.Errorf("account balance mismatch, has %d, want %d", balance, 2000)
	}
}

func TestEmptyWorkEthash(t *testing.T) {
	testEmptyWork(t, ethashChainConfig, ethash.NewFaker())
}
func TestEmptyWorkClique(t *testing.T) {
	testEmptyWork(t, cliqueChainConfig, clique.New(cliqueChainConfig.Clique, candb.NewMemDatabase()))
}

func testEmptyWork(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine) {
	defer engine.Close()

	w, _ := newTestWorker(t, chainConfig, engine)
	defer w.close()

	var (
		taskCh    = make(chan struct{}, 2)
		taskIndex int
	)

	checkEqual := func(t *testing.T, task *task, index int) {
		receiptLen, balance := 0, big.NewInt(0)
		if index == 1 {
			receiptLen, balance = 1, big.NewInt(1000)
		}
		if len(task.receipts) != receiptLen {
			t.Errorf("receipt number mismatch has %d, want %d", len(task.receipts), receiptLen)
		}
		if task.state.GetBalance(acc1Addr).Cmp(balance) != 0 {
			t.Errorf("account balance mismatch has %d, want %d", task.state.GetBalance(acc1Addr), balance)
		}
	}

	w.newTaskHook = func(task *task) {
		if task.block.NumberU64() == 1 {
			checkEqual(t, task, taskIndex)
			taskIndex += 1
			taskCh <- struct{}{}
		}
	}
	w.fullTaskInterval = func() {
		time.Sleep(100 * time.Millisecond)
	}

	for {
		b := w.pendingBlock()
		if b != nil && b.NumberU64() == 1 {
			break
		}
	}

	w.start()
	for i := 0; i < 2; i += 1 {
		to := time.NewTimer(time.Second)
		select {
		case <-taskCh:
		case <-to.C:
			t.Error("new task timeout")
		}
	}
}
