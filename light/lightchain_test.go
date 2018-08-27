package light

import (
	"context"
	"math/big"
	"testing"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/params"
)

var (
	canonicalSeed = 1
	forkSeed      = 2
)

func makeHeaderChain(parent *types.Header, n int, db candb.Database, seed int) []*types.Header {
	blocks, _ := kernel.GenerateChain(params.TestChainConfig, types.NewBlockWithHeader(parent), ethash.NewFaker(), db, n, func(i int, b *kernel.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(seed), 19: byte(i)})
	})
	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	return headers
}

func newCanonical(n int) (candb.Database, *LightChain, error) {
	db := candb.NewMemDatabase()
	gspec := kernel.Genesis{Config: params.TestChainConfig}
	genesis := gspec.MustCommit(db)
	blockchain, _ := NewLightChain(&dummyOdr{db: db}, gspec.Config, ethash.NewFaker())

	if n == 0 {
		return db, blockchain, nil
	}
	headers := makeHeaderChain(genesis.Header(), n, db, canonicalSeed)
	_, err := blockchain.InsertHeaderChain(headers, 1)
	return db, blockchain, err
}

func newTestLightChain() *LightChain {
	db := candb.NewMemDatabase()
	gspec := &kernel.Genesis{
		Difficulty: big.NewInt(1),
		Config:     params.TestChainConfig,
	}
	gspec.MustCommit(db)
	lc, err := NewLightChain(&dummyOdr{db: db}, gspec.Config, ethash.NewFullFaker())
	if err != nil {
		panic(err)
	}
	return lc
}

func testFork(t *testing.T, LightChain *LightChain, i, n int, comparator func(td1, td2 *big.Int)) {
	db, LightChain2, err := newCanonical(i)
	if err != nil {
		t.Fatal("could not make new canonical in testFork", err)
	}
	var hash1, hash2 common.Hash
	hash1 = LightChain.GetHeaderByNumber(uint64(i)).Hash()
	hash2 = LightChain2.GetHeaderByNumber(uint64(i)).Hash()
	if hash1 != hash2 {
		t.Errorf("chain content mismatch at %d: have hash %v, want hash %v", i, hash2, hash1)
	}
	headerChainB := makeHeaderChain(LightChain2.CurrentHeader(), n, db, forkSeed)
	if _, err := LightChain2.InsertHeaderChain(headerChainB, 1); err != nil {
		t.Fatalf("failed to insert forking chain: %v", err)
	}
	var tdPre, tdPost *big.Int

	tdPre = LightChain.GetTdByHash(LightChain.CurrentHeader().Hash())
	if err := testHeaderChainImport(headerChainB, LightChain); err != nil {
		t.Fatalf("failed to import forked header chain: %v", err)
	}
	tdPost = LightChain.GetTdByHash(headerChainB[len(headerChainB)-1].Hash())
	comparator(tdPre, tdPost)
}

func testHeaderChainImport(chain []*types.Header, lightchain *LightChain) error {
	for _, header := range chain {
		if err := lightchain.engine.VerifyHeader(lightchain.hc, header, true); err != nil {
			return err
		}
		lightchain.mu.Lock()
		rawdb.WriteTd(lightchain.chainDb, header.Hash(), header.Number.Uint64(), new(big.Int).Add(header.Difficulty, lightchain.GetTdByHash(header.ParentHash)))
		rawdb.WriteHeader(lightchain.chainDb, header)
		lightchain.mu.Unlock()
	}
	return nil
}

func TestExtendCanonicalHeaders(t *testing.T) {
	length := 5

	_, processor, err := newCanonical(length)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	better := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) <= 0 {
			t.Errorf("total difficulty mismatch: have %v, expected more than %v", td2, td1)
		}
	}
	testFork(t, processor, length, 1, better)
	testFork(t, processor, length, 2, better)
	testFork(t, processor, length, 5, better)
	testFork(t, processor, length, 10, better)
}

func TestShorterForkHeaders(t *testing.T) {
	length := 10

	_, processor, err := newCanonical(length)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	worse := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) >= 0 {
			t.Errorf("total difficulty mismatch: have %v, expected less than %v", td2, td1)
		}
	}
	testFork(t, processor, 0, 3, worse)
	testFork(t, processor, 0, 7, worse)
	testFork(t, processor, 1, 1, worse)
	testFork(t, processor, 1, 7, worse)
	testFork(t, processor, 5, 3, worse)
	testFork(t, processor, 5, 4, worse)
}

func TestLongerForkHeaders(t *testing.T) {
	length := 10

	_, processor, err := newCanonical(length)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	better := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) <= 0 {
			t.Errorf("total difficulty mismatch: have %v, expected more than %v", td2, td1)
		}
	}
	testFork(t, processor, 0, 11, better)
	testFork(t, processor, 0, 15, better)
	testFork(t, processor, 1, 10, better)
	testFork(t, processor, 1, 12, better)
	testFork(t, processor, 5, 6, better)
	testFork(t, processor, 5, 8, better)
}

func TestEqualForkHeaders(t *testing.T) {
	length := 10

	_, processor, err := newCanonical(length)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	equal := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) != 0 {
			t.Errorf("total difficulty mismatch: have %v, want %v", td2, td1)
		}
	}
	testFork(t, processor, 0, 10, equal)
	testFork(t, processor, 1, 9, equal)
	testFork(t, processor, 2, 8, equal)
	testFork(t, processor, 5, 5, equal)
	testFork(t, processor, 6, 4, equal)
	testFork(t, processor, 9, 1, equal)
}

func TestBrokenHeaderChain(t *testing.T) {
	db, LightChain, err := newCanonical(10)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	chain := makeHeaderChain(LightChain.CurrentHeader(), 5, db, forkSeed)[1:]
	if err := testHeaderChainImport(chain, LightChain); err == nil {
		t.Errorf("broken header chain not reported")
	}
}

func makeHeaderChainWithDiff(genesis *types.Block, d []int, seed byte) []*types.Header {
	var chain []*types.Header
	for i, difficulty := range d {
		header := &types.Header{
			Coinbase:    common.Address{seed},
			Number:      big.NewInt(int64(i + 1)),
			Difficulty:  big.NewInt(int64(difficulty)),
			UncleHash:   types.EmptyUncleHash,
			TxHash:      types.EmptyRootHash,
			ReceiptHash: types.EmptyRootHash,
		}
		if i == 0 {
			header.ParentHash = genesis.Hash()
		} else {
			header.ParentHash = chain[i-1].Hash()
		}
		chain = append(chain, types.CopyHeader(header))
	}
	return chain
}

type dummyOdr struct {
	OdrBackend
	db candb.Database
}

func (odr *dummyOdr) Database() candb.Database {
	return odr.db
}

func (odr *dummyOdr) Retrieve(ctx context.Context, req OdrRequest) error {
	return nil
}

func TestReorgLongHeaders(t *testing.T) {
	testReorg(t, []int{1, 2, 4}, []int{1, 2, 3, 4}, 10)
}

func TestReorgShortHeaders(t *testing.T) {
	testReorg(t, []int{1, 2, 3, 4}, []int{1, 10}, 11)
}

func testReorg(t *testing.T, first, second []int, td int64) {
	bc := newTestLightChain()

	bc.InsertHeaderChain(makeHeaderChainWithDiff(bc.genesisBlock, first, 11), 1)
	bc.InsertHeaderChain(makeHeaderChainWithDiff(bc.genesisBlock, second, 22), 1)
	prev := bc.CurrentHeader()
	for header := bc.GetHeaderByNumber(bc.CurrentHeader().Number.Uint64() - 1); header.Number.Uint64() != 0; prev, header = header, bc.GetHeaderByNumber(header.Number.Uint64()-1) {
		if prev.ParentHash != header.Hash() {
			t.Errorf("parent header hash mismatch: have %x, want %x", prev.ParentHash, header.Hash())
		}
	}
	want := new(big.Int).Add(bc.genesisBlock.Difficulty(), big.NewInt(td))
	if have := bc.GetTdByHash(bc.CurrentHeader().Hash()); have.Cmp(want) != 0 {
		t.Errorf("total difficulty mismatch: have %v, want %v", have, want)
	}
}

func TestBadHeaderHashes(t *testing.T) {
	bc := newTestLightChain()

	var err error
	headers := makeHeaderChainWithDiff(bc.genesisBlock, []int{1, 2, 4}, 10)
	kernel.BadHashes[headers[2].Hash()] = true
	if _, err = bc.InsertHeaderChain(headers, 1); err != kernel.ErrBlacklistedHash {
		t.Errorf("error mismatch: have: %v, want %v", err, kernel.ErrBlacklistedHash)
	}
}

func TestReorgBadHeaderHashes(t *testing.T) {
	bc := newTestLightChain()

	headers := makeHeaderChainWithDiff(bc.genesisBlock, []int{1, 2, 3, 4}, 10)

	if _, err := bc.InsertHeaderChain(headers, 1); err != nil {
		t.Fatalf("failed to import headers: %v", err)
	}
	if bc.CurrentHeader().Hash() != headers[3].Hash() {
		t.Errorf("last header hash mismatch: have: %x, want %x", bc.CurrentHeader().Hash(), headers[3].Hash())
	}
	kernel.BadHashes[headers[3].Hash()] = true
	defer func() { delete(kernel.BadHashes, headers[3].Hash()) }()

	ncm, err := NewLightChain(&dummyOdr{db: bc.chainDb}, params.TestChainConfig, ethash.NewFaker())
	if err != nil {
		t.Fatalf("failed to create new chain manager: %v", err)
	}
	if ncm.CurrentHeader().Hash() != headers[2].Hash() {
		t.Errorf("last header hash mismatch: have: %x, want %x", ncm.CurrentHeader().Hash(), headers[2].Hash())
	}
}
