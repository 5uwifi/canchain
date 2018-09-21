package kernel

import (
	"math/big"
	"testing"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/params"
)

func TestDAOForkRangeExtradata(t *testing.T) {
	forkBlock := big.NewInt(32)

	db := candb.NewMemDatabase()
	gspec := new(Genesis)
	genesis := gspec.MustCommit(db)
	prefix, _ := GenerateChain(params.TestChainConfig, genesis, ethash.NewFaker(), db, int(forkBlock.Int64()-1), func(i int, gen *BlockGen) {})

	proDb := candb.NewMemDatabase()
	gspec.MustCommit(proDb)

	proConf := *params.TestChainConfig
	proConf.DAOForkBlock = forkBlock
	proConf.DAOForkSupport = true

	proBc, _ := NewBlockChain(proDb, nil, &proConf, ethash.NewFaker(), vm.Config{}, nil)
	defer proBc.Stop()

	conDb := candb.NewMemDatabase()
	gspec.MustCommit(conDb)

	conConf := *params.TestChainConfig
	conConf.DAOForkBlock = forkBlock
	conConf.DAOForkSupport = false

	conBc, _ := NewBlockChain(conDb, nil, &conConf, ethash.NewFaker(), vm.Config{}, nil)
	defer conBc.Stop()

	if _, err := proBc.InsertChain(prefix); err != nil {
		t.Fatalf("pro-fork: failed to import chain prefix: %v", err)
	}
	if _, err := conBc.InsertChain(prefix); err != nil {
		t.Fatalf("con-fork: failed to import chain prefix: %v", err)
	}
	for i := int64(0); i < params.DAOForkExtraRange.Int64(); i++ {
		db = candb.NewMemDatabase()
		gspec.MustCommit(db)
		bc, _ := NewBlockChain(db, nil, &conConf, ethash.NewFaker(), vm.Config{}, nil)
		defer bc.Stop()

		blocks := conBc.GetBlocksFromHash(conBc.CurrentBlock().Hash(), int(conBc.CurrentBlock().NumberU64()))
		for j := 0; j < len(blocks)/2; j++ {
			blocks[j], blocks[len(blocks)-1-j] = blocks[len(blocks)-1-j], blocks[j]
		}
		if _, err := bc.InsertChain(blocks); err != nil {
			t.Fatalf("failed to import contra-fork chain for expansion: %v", err)
		}
		if err := bc.stateCache.TrieDB().Commit(bc.CurrentHeader().Root, true); err != nil {
			t.Fatalf("failed to commit contra-fork head for expansion: %v", err)
		}
		blocks, _ = GenerateChain(&proConf, conBc.CurrentBlock(), ethash.NewFaker(), db, 1, func(i int, gen *BlockGen) {})
		if _, err := conBc.InsertChain(blocks); err == nil {
			t.Fatalf("contra-fork chain accepted pro-fork block: %v", blocks[0])
		}
		blocks, _ = GenerateChain(&conConf, conBc.CurrentBlock(), ethash.NewFaker(), db, 1, func(i int, gen *BlockGen) {})
		if _, err := conBc.InsertChain(blocks); err != nil {
			t.Fatalf("contra-fork chain didn't accepted no-fork block: %v", err)
		}
		db = candb.NewMemDatabase()
		gspec.MustCommit(db)
		bc, _ = NewBlockChain(db, nil, &proConf, ethash.NewFaker(), vm.Config{}, nil)
		defer bc.Stop()

		blocks = proBc.GetBlocksFromHash(proBc.CurrentBlock().Hash(), int(proBc.CurrentBlock().NumberU64()))
		for j := 0; j < len(blocks)/2; j++ {
			blocks[j], blocks[len(blocks)-1-j] = blocks[len(blocks)-1-j], blocks[j]
		}
		if _, err := bc.InsertChain(blocks); err != nil {
			t.Fatalf("failed to import pro-fork chain for expansion: %v", err)
		}
		if err := bc.stateCache.TrieDB().Commit(bc.CurrentHeader().Root, true); err != nil {
			t.Fatalf("failed to commit pro-fork head for expansion: %v", err)
		}
		blocks, _ = GenerateChain(&conConf, proBc.CurrentBlock(), ethash.NewFaker(), db, 1, func(i int, gen *BlockGen) {})
		if _, err := proBc.InsertChain(blocks); err == nil {
			t.Fatalf("pro-fork chain accepted contra-fork block: %v", blocks[0])
		}
		blocks, _ = GenerateChain(&proConf, proBc.CurrentBlock(), ethash.NewFaker(), db, 1, func(i int, gen *BlockGen) {})
		if _, err := proBc.InsertChain(blocks); err != nil {
			t.Fatalf("pro-fork chain didn't accepted pro-fork block: %v", err)
		}
	}
	db = candb.NewMemDatabase()
	gspec.MustCommit(db)
	bc, _ := NewBlockChain(db, nil, &conConf, ethash.NewFaker(), vm.Config{}, nil)
	defer bc.Stop()

	blocks := conBc.GetBlocksFromHash(conBc.CurrentBlock().Hash(), int(conBc.CurrentBlock().NumberU64()))
	for j := 0; j < len(blocks)/2; j++ {
		blocks[j], blocks[len(blocks)-1-j] = blocks[len(blocks)-1-j], blocks[j]
	}
	if _, err := bc.InsertChain(blocks); err != nil {
		t.Fatalf("failed to import contra-fork chain for expansion: %v", err)
	}
	if err := bc.stateCache.TrieDB().Commit(bc.CurrentHeader().Root, true); err != nil {
		t.Fatalf("failed to commit contra-fork head for expansion: %v", err)
	}
	blocks, _ = GenerateChain(&proConf, conBc.CurrentBlock(), ethash.NewFaker(), db, 1, func(i int, gen *BlockGen) {})
	if _, err := conBc.InsertChain(blocks); err != nil {
		t.Fatalf("contra-fork chain didn't accept pro-fork block post-fork: %v", err)
	}
	db = candb.NewMemDatabase()
	gspec.MustCommit(db)
	bc, _ = NewBlockChain(db, nil, &proConf, ethash.NewFaker(), vm.Config{}, nil)
	defer bc.Stop()

	blocks = proBc.GetBlocksFromHash(proBc.CurrentBlock().Hash(), int(proBc.CurrentBlock().NumberU64()))
	for j := 0; j < len(blocks)/2; j++ {
		blocks[j], blocks[len(blocks)-1-j] = blocks[len(blocks)-1-j], blocks[j]
	}
	if _, err := bc.InsertChain(blocks); err != nil {
		t.Fatalf("failed to import pro-fork chain for expansion: %v", err)
	}
	if err := bc.stateCache.TrieDB().Commit(bc.CurrentHeader().Root, true); err != nil {
		t.Fatalf("failed to commit pro-fork head for expansion: %v", err)
	}
	blocks, _ = GenerateChain(&conConf, proBc.CurrentBlock(), ethash.NewFaker(), db, 1, func(i int, gen *BlockGen) {})
	if _, err := proBc.InsertChain(blocks); err != nil {
		t.Fatalf("pro-fork chain didn't accept contra-fork block post-fork: %v", err)
	}
}
