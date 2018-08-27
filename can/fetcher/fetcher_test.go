package fetcher

import (
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/params"
)

var (
	testdb       = candb.NewMemDatabase()
	testKey, _   = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	testAddress  = crypto.PubkeyToAddress(testKey.PublicKey)
	genesis      = kernel.GenesisBlockForTesting(testdb, testAddress, big.NewInt(1000000000))
	unknownBlock = types.NewBlock(&types.Header{GasLimit: params.GenesisGasLimit}, nil, nil, nil)
)

func makeChain(n int, seed byte, parent *types.Block) ([]common.Hash, map[common.Hash]*types.Block) {
	blocks, _ := kernel.GenerateChain(params.TestChainConfig, parent, ethash.NewFaker(), testdb, n, func(i int, block *kernel.BlockGen) {
		block.SetCoinbase(common.Address{seed})

		if parent == genesis && i%3 == 0 {
			signer := types.MakeSigner(params.TestChainConfig, block.Number())
			tx, err := types.SignTx(types.NewTransaction(block.TxNonce(testAddress), common.Address{seed}, big.NewInt(1000), params.TxGas, nil, nil), signer, testKey)
			if err != nil {
				panic(err)
			}
			block.AddTx(tx)
		}
		if i%5 == 0 {
			block.AddUncle(&types.Header{ParentHash: block.PrevBlock(i - 1).Hash(), Number: big.NewInt(int64(i - 1))})
		}
	})
	hashes := make([]common.Hash, n+1)
	hashes[len(hashes)-1] = parent.Hash()
	blockm := make(map[common.Hash]*types.Block, n+1)
	blockm[parent.Hash()] = parent
	for i, b := range blocks {
		hashes[len(hashes)-i-2] = b.Hash()
		blockm[b.Hash()] = b
	}
	return hashes, blockm
}

type fetcherTester struct {
	fetcher *Fetcher

	hashes []common.Hash
	blocks map[common.Hash]*types.Block
	drops  map[string]bool

	lock sync.RWMutex
}

func newTester() *fetcherTester {
	tester := &fetcherTester{
		hashes: []common.Hash{genesis.Hash()},
		blocks: map[common.Hash]*types.Block{genesis.Hash(): genesis},
		drops:  make(map[string]bool),
	}
	tester.fetcher = New(tester.getBlock, tester.verifyHeader, tester.broadcastBlock, tester.chainHeight, tester.insertChain, tester.dropPeer)
	tester.fetcher.Start()

	return tester
}

func (f *fetcherTester) getBlock(hash common.Hash) *types.Block {
	f.lock.RLock()
	defer f.lock.RUnlock()

	return f.blocks[hash]
}

func (f *fetcherTester) verifyHeader(header *types.Header) error {
	return nil
}

func (f *fetcherTester) broadcastBlock(block *types.Block, propagate bool) {
}

func (f *fetcherTester) chainHeight() uint64 {
	f.lock.RLock()
	defer f.lock.RUnlock()

	return f.blocks[f.hashes[len(f.hashes)-1]].NumberU64()
}

func (f *fetcherTester) insertChain(blocks types.Blocks) (int, error) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for i, block := range blocks {
		if _, ok := f.blocks[block.ParentHash()]; !ok {
			return i, errors.New("unknown parent")
		}
		if block.NumberU64() <= f.blocks[f.hashes[len(f.hashes)-1]].NumberU64() {
			return i, nil
		}
		f.hashes = append(f.hashes, block.Hash())
		f.blocks[block.Hash()] = block
	}
	return 0, nil
}

func (f *fetcherTester) dropPeer(peer string) {
	f.lock.Lock()
	defer f.lock.Unlock()

	f.drops[peer] = true
}

func (f *fetcherTester) makeHeaderFetcher(peer string, blocks map[common.Hash]*types.Block, drift time.Duration) headerRequesterFn {
	closure := make(map[common.Hash]*types.Block)
	for hash, block := range blocks {
		closure[hash] = block
	}
	return func(hash common.Hash) error {
		headers := make([]*types.Header, 0, 1)
		if block, ok := closure[hash]; ok {
			headers = append(headers, block.Header())
		}
		go f.fetcher.FilterHeaders(peer, headers, time.Now().Add(drift))

		return nil
	}
}

func (f *fetcherTester) makeBodyFetcher(peer string, blocks map[common.Hash]*types.Block, drift time.Duration) bodyRequesterFn {
	closure := make(map[common.Hash]*types.Block)
	for hash, block := range blocks {
		closure[hash] = block
	}
	return func(hashes []common.Hash) error {
		transactions := make([][]*types.Transaction, 0, len(hashes))
		uncles := make([][]*types.Header, 0, len(hashes))

		for _, hash := range hashes {
			if block, ok := closure[hash]; ok {
				transactions = append(transactions, block.Transactions())
				uncles = append(uncles, block.Uncles())
			}
		}
		go f.fetcher.FilterBodies(peer, transactions, uncles, time.Now().Add(drift))

		return nil
	}
}

func verifyFetchingEvent(t *testing.T, fetching chan []common.Hash, arrive bool) {
	if arrive {
		select {
		case <-fetching:
		case <-time.After(time.Second):
			t.Fatalf("fetching timeout")
		}
	} else {
		select {
		case <-fetching:
			t.Fatalf("fetching invoked")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func verifyCompletingEvent(t *testing.T, completing chan []common.Hash, arrive bool) {
	if arrive {
		select {
		case <-completing:
		case <-time.After(time.Second):
			t.Fatalf("completing timeout")
		}
	} else {
		select {
		case <-completing:
			t.Fatalf("completing invoked")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func verifyImportEvent(t *testing.T, imported chan *types.Block, arrive bool) {
	if arrive {
		select {
		case <-imported:
		case <-time.After(time.Second):
			t.Fatalf("import timeout")
		}
	} else {
		select {
		case <-imported:
			t.Fatalf("import invoked")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func verifyImportCount(t *testing.T, imported chan *types.Block, count int) {
	for i := 0; i < count; i++ {
		select {
		case <-imported:
		case <-time.After(time.Second):
			t.Fatalf("block %d: import timeout", i+1)
		}
	}
	verifyImportDone(t, imported)
}

func verifyImportDone(t *testing.T, imported chan *types.Block) {
	select {
	case <-imported:
		t.Fatalf("extra block imported")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSequentialAnnouncements62(t *testing.T) { testSequentialAnnouncements(t, 62) }
func TestSequentialAnnouncements63(t *testing.T) { testSequentialAnnouncements(t, 63) }
func TestSequentialAnnouncements64(t *testing.T) { testSequentialAnnouncements(t, 64) }

func testSequentialAnnouncements(t *testing.T, protocol int) {
	targetBlocks := 4 * hashLimit
	hashes, blocks := makeChain(targetBlocks, 0, genesis)

	tester := newTester()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)

	imported := make(chan *types.Block)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	for i := len(hashes) - 2; i >= 0; i-- {
		tester.fetcher.Notify("valid", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
		verifyImportEvent(t, imported, true)
	}
	verifyImportDone(t, imported)
}

func TestConcurrentAnnouncements62(t *testing.T) { testConcurrentAnnouncements(t, 62) }
func TestConcurrentAnnouncements63(t *testing.T) { testConcurrentAnnouncements(t, 63) }
func TestConcurrentAnnouncements64(t *testing.T) { testConcurrentAnnouncements(t, 64) }

func testConcurrentAnnouncements(t *testing.T, protocol int) {
	targetBlocks := 4 * hashLimit
	hashes, blocks := makeChain(targetBlocks, 0, genesis)

	tester := newTester()
	firstHeaderFetcher := tester.makeHeaderFetcher("first", blocks, -gatherSlack)
	firstBodyFetcher := tester.makeBodyFetcher("first", blocks, 0)
	secondHeaderFetcher := tester.makeHeaderFetcher("second", blocks, -gatherSlack)
	secondBodyFetcher := tester.makeBodyFetcher("second", blocks, 0)

	counter := uint32(0)
	firstHeaderWrapper := func(hash common.Hash) error {
		atomic.AddUint32(&counter, 1)
		return firstHeaderFetcher(hash)
	}
	secondHeaderWrapper := func(hash common.Hash) error {
		atomic.AddUint32(&counter, 1)
		return secondHeaderFetcher(hash)
	}
	imported := make(chan *types.Block)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	for i := len(hashes) - 2; i >= 0; i-- {
		tester.fetcher.Notify("first", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout), firstHeaderWrapper, firstBodyFetcher)
		tester.fetcher.Notify("second", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout+time.Millisecond), secondHeaderWrapper, secondBodyFetcher)
		tester.fetcher.Notify("second", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout-time.Millisecond), secondHeaderWrapper, secondBodyFetcher)
		verifyImportEvent(t, imported, true)
	}
	verifyImportDone(t, imported)

	if int(counter) != targetBlocks {
		t.Fatalf("retrieval count mismatch: have %v, want %v", counter, targetBlocks)
	}
}

func TestOverlappingAnnouncements62(t *testing.T) { testOverlappingAnnouncements(t, 62) }
func TestOverlappingAnnouncements63(t *testing.T) { testOverlappingAnnouncements(t, 63) }
func TestOverlappingAnnouncements64(t *testing.T) { testOverlappingAnnouncements(t, 64) }

func testOverlappingAnnouncements(t *testing.T, protocol int) {
	targetBlocks := 4 * hashLimit
	hashes, blocks := makeChain(targetBlocks, 0, genesis)

	tester := newTester()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)

	overlap := 16
	imported := make(chan *types.Block, len(hashes)-1)
	for i := 0; i < overlap; i++ {
		imported <- nil
	}
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	for i := len(hashes) - 2; i >= 0; i-- {
		tester.fetcher.Notify("valid", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
		select {
		case <-imported:
		case <-time.After(time.Second):
			t.Fatalf("block %d: import timeout", len(hashes)-i)
		}
	}
	verifyImportCount(t, imported, overlap)
}

func TestPendingDeduplication62(t *testing.T) { testPendingDeduplication(t, 62) }
func TestPendingDeduplication63(t *testing.T) { testPendingDeduplication(t, 63) }
func TestPendingDeduplication64(t *testing.T) { testPendingDeduplication(t, 64) }

func testPendingDeduplication(t *testing.T, protocol int) {
	hashes, blocks := makeChain(1, 0, genesis)

	tester := newTester()
	headerFetcher := tester.makeHeaderFetcher("repeater", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("repeater", blocks, 0)

	delay := 50 * time.Millisecond
	counter := uint32(0)
	headerWrapper := func(hash common.Hash) error {
		atomic.AddUint32(&counter, 1)

		go func() {
			time.Sleep(delay)
			headerFetcher(hash)
		}()
		return nil
	}
	for tester.getBlock(hashes[0]) == nil {
		tester.fetcher.Notify("repeater", hashes[0], 1, time.Now().Add(-arriveTimeout), headerWrapper, bodyFetcher)
		time.Sleep(time.Millisecond)
	}
	time.Sleep(delay)

	if imported := len(tester.blocks); imported != 2 {
		t.Fatalf("synchronised block mismatch: have %v, want %v", imported, 2)
	}
	if int(counter) != 1 {
		t.Fatalf("retrieval count mismatch: have %v, want %v", counter, 1)
	}
}

func TestRandomArrivalImport62(t *testing.T) { testRandomArrivalImport(t, 62) }
func TestRandomArrivalImport63(t *testing.T) { testRandomArrivalImport(t, 63) }
func TestRandomArrivalImport64(t *testing.T) { testRandomArrivalImport(t, 64) }

func testRandomArrivalImport(t *testing.T, protocol int) {
	targetBlocks := maxQueueDist
	hashes, blocks := makeChain(targetBlocks, 0, genesis)
	skip := targetBlocks / 2

	tester := newTester()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)

	imported := make(chan *types.Block, len(hashes)-1)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	for i := len(hashes) - 1; i >= 0; i-- {
		if i != skip {
			tester.fetcher.Notify("valid", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
			time.Sleep(time.Millisecond)
		}
	}
	tester.fetcher.Notify("valid", hashes[skip], uint64(len(hashes)-skip-1), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
	verifyImportCount(t, imported, len(hashes)-1)
}

func TestQueueGapFill62(t *testing.T) { testQueueGapFill(t, 62) }
func TestQueueGapFill63(t *testing.T) { testQueueGapFill(t, 63) }
func TestQueueGapFill64(t *testing.T) { testQueueGapFill(t, 64) }

func testQueueGapFill(t *testing.T, protocol int) {
	targetBlocks := maxQueueDist
	hashes, blocks := makeChain(targetBlocks, 0, genesis)
	skip := targetBlocks / 2

	tester := newTester()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)

	imported := make(chan *types.Block, len(hashes)-1)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	for i := len(hashes) - 1; i >= 0; i-- {
		if i != skip {
			tester.fetcher.Notify("valid", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
			time.Sleep(time.Millisecond)
		}
	}
	tester.fetcher.Enqueue("valid", blocks[hashes[skip]])
	verifyImportCount(t, imported, len(hashes)-1)
}

func TestImportDeduplication62(t *testing.T) { testImportDeduplication(t, 62) }
func TestImportDeduplication63(t *testing.T) { testImportDeduplication(t, 63) }
func TestImportDeduplication64(t *testing.T) { testImportDeduplication(t, 64) }

func testImportDeduplication(t *testing.T, protocol int) {
	hashes, blocks := makeChain(2, 0, genesis)

	tester := newTester()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)

	counter := uint32(0)
	tester.fetcher.insertChain = func(blocks types.Blocks) (int, error) {
		atomic.AddUint32(&counter, uint32(len(blocks)))
		return tester.insertChain(blocks)
	}
	fetching := make(chan []common.Hash)
	imported := make(chan *types.Block, len(hashes)-1)
	tester.fetcher.fetchingHook = func(hashes []common.Hash) { fetching <- hashes }
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	tester.fetcher.Notify("valid", hashes[0], 1, time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
	<-fetching

	tester.fetcher.Enqueue("valid", blocks[hashes[0]])
	tester.fetcher.Enqueue("valid", blocks[hashes[0]])
	tester.fetcher.Enqueue("valid", blocks[hashes[0]])

	tester.fetcher.Enqueue("valid", blocks[hashes[1]])
	verifyImportCount(t, imported, 2)

	if counter != 2 {
		t.Fatalf("import invocation count mismatch: have %v, want %v", counter, 2)
	}
}

func TestDistantPropagationDiscarding(t *testing.T) {
	hashes, blocks := makeChain(3*maxQueueDist, 0, genesis)
	head := hashes[len(hashes)/2]

	low, high := len(hashes)/2+maxUncleDist+1, len(hashes)/2-maxQueueDist-1

	tester := newTester()

	tester.lock.Lock()
	tester.hashes = []common.Hash{head}
	tester.blocks = map[common.Hash]*types.Block{head: blocks[head]}
	tester.lock.Unlock()

	tester.fetcher.Enqueue("lower", blocks[hashes[low]])
	time.Sleep(10 * time.Millisecond)
	if !tester.fetcher.queue.Empty() {
		t.Fatalf("fetcher queued stale block")
	}
	tester.fetcher.Enqueue("higher", blocks[hashes[high]])
	time.Sleep(10 * time.Millisecond)
	if !tester.fetcher.queue.Empty() {
		t.Fatalf("fetcher queued future block")
	}
}

func TestDistantAnnouncementDiscarding62(t *testing.T) { testDistantAnnouncementDiscarding(t, 62) }
func TestDistantAnnouncementDiscarding63(t *testing.T) { testDistantAnnouncementDiscarding(t, 63) }
func TestDistantAnnouncementDiscarding64(t *testing.T) { testDistantAnnouncementDiscarding(t, 64) }

func testDistantAnnouncementDiscarding(t *testing.T, protocol int) {
	hashes, blocks := makeChain(3*maxQueueDist, 0, genesis)
	head := hashes[len(hashes)/2]

	low, high := len(hashes)/2+maxUncleDist+1, len(hashes)/2-maxQueueDist-1

	tester := newTester()

	tester.lock.Lock()
	tester.hashes = []common.Hash{head}
	tester.blocks = map[common.Hash]*types.Block{head: blocks[head]}
	tester.lock.Unlock()

	headerFetcher := tester.makeHeaderFetcher("lower", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("lower", blocks, 0)

	fetching := make(chan struct{}, 2)
	tester.fetcher.fetchingHook = func(hashes []common.Hash) { fetching <- struct{}{} }

	tester.fetcher.Notify("lower", hashes[low], blocks[hashes[low]].NumberU64(), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
	select {
	case <-time.After(50 * time.Millisecond):
	case <-fetching:
		t.Fatalf("fetcher requested stale header")
	}
	tester.fetcher.Notify("higher", hashes[high], blocks[hashes[high]].NumberU64(), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)
	select {
	case <-time.After(50 * time.Millisecond):
	case <-fetching:
		t.Fatalf("fetcher requested future header")
	}
}

func TestInvalidNumberAnnouncement62(t *testing.T) { testInvalidNumberAnnouncement(t, 62) }
func TestInvalidNumberAnnouncement63(t *testing.T) { testInvalidNumberAnnouncement(t, 63) }
func TestInvalidNumberAnnouncement64(t *testing.T) { testInvalidNumberAnnouncement(t, 64) }

func testInvalidNumberAnnouncement(t *testing.T, protocol int) {
	hashes, blocks := makeChain(1, 0, genesis)

	tester := newTester()
	badHeaderFetcher := tester.makeHeaderFetcher("bad", blocks, -gatherSlack)
	badBodyFetcher := tester.makeBodyFetcher("bad", blocks, 0)

	imported := make(chan *types.Block)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	tester.fetcher.Notify("bad", hashes[0], 2, time.Now().Add(-arriveTimeout), badHeaderFetcher, badBodyFetcher)
	verifyImportEvent(t, imported, false)

	tester.lock.RLock()
	dropped := tester.drops["bad"]
	tester.lock.RUnlock()

	if !dropped {
		t.Fatalf("peer with invalid numbered announcement not dropped")
	}

	goodHeaderFetcher := tester.makeHeaderFetcher("good", blocks, -gatherSlack)
	goodBodyFetcher := tester.makeBodyFetcher("good", blocks, 0)
	tester.fetcher.Notify("good", hashes[0], 1, time.Now().Add(-arriveTimeout), goodHeaderFetcher, goodBodyFetcher)
	verifyImportEvent(t, imported, true)

	tester.lock.RLock()
	dropped = tester.drops["good"]
	tester.lock.RUnlock()

	if dropped {
		t.Fatalf("peer with valid numbered announcement dropped")
	}
	verifyImportDone(t, imported)
}

func TestEmptyBlockShortCircuit62(t *testing.T) { testEmptyBlockShortCircuit(t, 62) }
func TestEmptyBlockShortCircuit63(t *testing.T) { testEmptyBlockShortCircuit(t, 63) }
func TestEmptyBlockShortCircuit64(t *testing.T) { testEmptyBlockShortCircuit(t, 64) }

func testEmptyBlockShortCircuit(t *testing.T, protocol int) {
	hashes, blocks := makeChain(32, 0, genesis)

	tester := newTester()
	headerFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	bodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)

	fetching := make(chan []common.Hash)
	tester.fetcher.fetchingHook = func(hashes []common.Hash) { fetching <- hashes }

	completing := make(chan []common.Hash)
	tester.fetcher.completingHook = func(hashes []common.Hash) { completing <- hashes }

	imported := make(chan *types.Block)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }

	for i := len(hashes) - 2; i >= 0; i-- {
		tester.fetcher.Notify("valid", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout), headerFetcher, bodyFetcher)

		verifyFetchingEvent(t, fetching, true)

		verifyCompletingEvent(t, completing, len(blocks[hashes[i]].Transactions()) > 0 || len(blocks[hashes[i]].Uncles()) > 0)

		verifyImportEvent(t, imported, true)
	}
	verifyImportDone(t, imported)
}

func TestHashMemoryExhaustionAttack62(t *testing.T) { testHashMemoryExhaustionAttack(t, 62) }
func TestHashMemoryExhaustionAttack63(t *testing.T) { testHashMemoryExhaustionAttack(t, 63) }
func TestHashMemoryExhaustionAttack64(t *testing.T) { testHashMemoryExhaustionAttack(t, 64) }

func testHashMemoryExhaustionAttack(t *testing.T, protocol int) {
	tester := newTester()

	imported, announces := make(chan *types.Block), int32(0)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }
	tester.fetcher.announceChangeHook = func(hash common.Hash, added bool) {
		if added {
			atomic.AddInt32(&announces, 1)
		} else {
			atomic.AddInt32(&announces, -1)
		}
	}
	targetBlocks := hashLimit + 2*maxQueueDist
	hashes, blocks := makeChain(targetBlocks, 0, genesis)
	validHeaderFetcher := tester.makeHeaderFetcher("valid", blocks, -gatherSlack)
	validBodyFetcher := tester.makeBodyFetcher("valid", blocks, 0)

	attack, _ := makeChain(targetBlocks, 0, unknownBlock)
	attackerHeaderFetcher := tester.makeHeaderFetcher("attacker", nil, -gatherSlack)
	attackerBodyFetcher := tester.makeBodyFetcher("attacker", nil, 0)

	for i := 0; i < len(attack); i++ {
		if i < maxQueueDist {
			tester.fetcher.Notify("valid", hashes[len(hashes)-2-i], uint64(i+1), time.Now(), validHeaderFetcher, validBodyFetcher)
		}
		tester.fetcher.Notify("attacker", attack[i], 1 /* don't distance drop */, time.Now(), attackerHeaderFetcher, attackerBodyFetcher)
	}
	if count := atomic.LoadInt32(&announces); count != hashLimit+maxQueueDist {
		t.Fatalf("queued announce count mismatch: have %d, want %d", count, hashLimit+maxQueueDist)
	}
	verifyImportCount(t, imported, maxQueueDist)

	for i := len(hashes) - maxQueueDist - 2; i >= 0; i-- {
		tester.fetcher.Notify("valid", hashes[i], uint64(len(hashes)-i-1), time.Now().Add(-arriveTimeout), validHeaderFetcher, validBodyFetcher)
		verifyImportEvent(t, imported, true)
	}
	verifyImportDone(t, imported)
}

func TestBlockMemoryExhaustionAttack(t *testing.T) {
	tester := newTester()

	imported, enqueued := make(chan *types.Block), int32(0)
	tester.fetcher.importedHook = func(block *types.Block) { imported <- block }
	tester.fetcher.queueChangeHook = func(hash common.Hash, added bool) {
		if added {
			atomic.AddInt32(&enqueued, 1)
		} else {
			atomic.AddInt32(&enqueued, -1)
		}
	}
	targetBlocks := hashLimit + 2*maxQueueDist
	hashes, blocks := makeChain(targetBlocks, 0, genesis)
	attack := make(map[common.Hash]*types.Block)
	for i := byte(0); len(attack) < blockLimit+2*maxQueueDist; i++ {
		hashes, blocks := makeChain(maxQueueDist-1, i, unknownBlock)
		for _, hash := range hashes[:maxQueueDist-2] {
			attack[hash] = blocks[hash]
		}
	}
	for _, block := range attack {
		tester.fetcher.Enqueue("attacker", block)
	}
	time.Sleep(200 * time.Millisecond)
	if queued := atomic.LoadInt32(&enqueued); queued != blockLimit {
		t.Fatalf("queued block count mismatch: have %d, want %d", queued, blockLimit)
	}
	for i := 0; i < maxQueueDist-1; i++ {
		tester.fetcher.Enqueue("valid", blocks[hashes[len(hashes)-3-i]])
	}
	time.Sleep(100 * time.Millisecond)
	if queued := atomic.LoadInt32(&enqueued); queued != blockLimit+maxQueueDist-1 {
		t.Fatalf("queued block count mismatch: have %d, want %d", queued, blockLimit+maxQueueDist-1)
	}
	tester.fetcher.Enqueue("valid", blocks[hashes[len(hashes)-2]])
	verifyImportCount(t, imported, maxQueueDist)

	for i := maxQueueDist; i < len(hashes)-1; i++ {
		tester.fetcher.Enqueue("valid", blocks[hashes[len(hashes)-2-i]])
		verifyImportEvent(t, imported, true)
	}
	verifyImportDone(t, imported)
}
