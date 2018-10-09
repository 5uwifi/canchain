package downloader

import (
	"errors"
	"fmt"
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
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/trie"
	"github.com/5uwifi/canchain/params"
)

var (
	testKey, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	testAddress = crypto.PubkeyToAddress(testKey.PublicKey)
)

func init() {
	MaxForkAncestry = uint64(10000)
	blockCacheItems = 1024
	fsHeaderContCheck = 500 * time.Millisecond
}

type downloadTester struct {
	downloader *Downloader

	genesis *types.Block
	stateDb candb.Database
	peerDb  candb.Database

	ownHashes   []common.Hash
	ownHeaders  map[common.Hash]*types.Header
	ownBlocks   map[common.Hash]*types.Block
	ownReceipts map[common.Hash]types.Receipts
	ownChainTd  map[common.Hash]*big.Int

	peerHashes   map[string][]common.Hash
	peerHeaders  map[string]map[common.Hash]*types.Header
	peerBlocks   map[string]map[common.Hash]*types.Block
	peerReceipts map[string]map[common.Hash]types.Receipts
	peerChainTds map[string]map[common.Hash]*big.Int

	peerMissingStates map[string]map[common.Hash]bool

	lock sync.RWMutex
}

func newTester() *downloadTester {
	testdb := candb.NewMemDatabase()
	genesis := kernel.GenesisBlockForTesting(testdb, testAddress, big.NewInt(1000000000))

	tester := &downloadTester{
		genesis:           genesis,
		peerDb:            testdb,
		ownHashes:         []common.Hash{genesis.Hash()},
		ownHeaders:        map[common.Hash]*types.Header{genesis.Hash(): genesis.Header()},
		ownBlocks:         map[common.Hash]*types.Block{genesis.Hash(): genesis},
		ownReceipts:       map[common.Hash]types.Receipts{genesis.Hash(): nil},
		ownChainTd:        map[common.Hash]*big.Int{genesis.Hash(): genesis.Difficulty()},
		peerHashes:        make(map[string][]common.Hash),
		peerHeaders:       make(map[string]map[common.Hash]*types.Header),
		peerBlocks:        make(map[string]map[common.Hash]*types.Block),
		peerReceipts:      make(map[string]map[common.Hash]types.Receipts),
		peerChainTds:      make(map[string]map[common.Hash]*big.Int),
		peerMissingStates: make(map[string]map[common.Hash]bool),
	}
	tester.stateDb = candb.NewMemDatabase()
	tester.stateDb.Put(genesis.Root().Bytes(), []byte{0x00})

	tester.downloader = New(FullSync, tester.stateDb, new(event.TypeMux), tester, nil, tester.dropPeer)

	return tester
}

func (dl *downloadTester) makeChain(n int, seed byte, parent *types.Block, parentReceipts types.Receipts, heavy bool) ([]common.Hash, map[common.Hash]*types.Header, map[common.Hash]*types.Block, map[common.Hash]types.Receipts) {
	blocks, receipts := kernel.GenerateChain(params.TestChainConfig, parent, ethash.NewFaker(), dl.peerDb, n, func(i int, block *kernel.BlockGen) {
		block.SetCoinbase(common.Address{seed})

		if heavy {
			block.OffsetTime(-1)
		}
		if parent == dl.genesis && i%3 == 0 {
			signer := types.MakeSigner(params.TestChainConfig, block.Number())
			tx, err := types.SignTx(types.NewTransaction(block.TxNonce(testAddress), common.Address{seed}, big.NewInt(1000), params.TxGas, nil, nil), signer, testKey)
			if err != nil {
				panic(err)
			}
			block.AddTx(tx)
		}
		if i > 0 && i%5 == 0 {
			block.AddUncle(&types.Header{
				ParentHash: block.PrevBlock(i - 1).Hash(),
				Number:     big.NewInt(block.Number().Int64() - 1),
			})
		}
	})
	hashes := make([]common.Hash, n+1)
	hashes[len(hashes)-1] = parent.Hash()

	headerm := make(map[common.Hash]*types.Header, n+1)
	headerm[parent.Hash()] = parent.Header()

	blockm := make(map[common.Hash]*types.Block, n+1)
	blockm[parent.Hash()] = parent

	receiptm := make(map[common.Hash]types.Receipts, n+1)
	receiptm[parent.Hash()] = parentReceipts

	for i, b := range blocks {
		hashes[len(hashes)-i-2] = b.Hash()
		headerm[b.Hash()] = b.Header()
		blockm[b.Hash()] = b
		receiptm[b.Hash()] = receipts[i]
	}
	return hashes, headerm, blockm, receiptm
}

func (dl *downloadTester) makeChainFork(n, f int, parent *types.Block, parentReceipts types.Receipts, balanced bool) ([]common.Hash, []common.Hash, map[common.Hash]*types.Header, map[common.Hash]*types.Header, map[common.Hash]*types.Block, map[common.Hash]*types.Block, map[common.Hash]types.Receipts, map[common.Hash]types.Receipts) {
	hashes, headers, blocks, receipts := dl.makeChain(n-f, 0, parent, parentReceipts, false)

	hashes1, headers1, blocks1, receipts1 := dl.makeChain(f, 1, blocks[hashes[0]], receipts[hashes[0]], false)
	hashes1 = append(hashes1, hashes[1:]...)

	heavy := false
	if !balanced {
		heavy = true
	}
	hashes2, headers2, blocks2, receipts2 := dl.makeChain(f, 2, blocks[hashes[0]], receipts[hashes[0]], heavy)
	hashes2 = append(hashes2, hashes[1:]...)

	for hash, header := range headers {
		headers1[hash] = header
		headers2[hash] = header
	}
	for hash, block := range blocks {
		blocks1[hash] = block
		blocks2[hash] = block
	}
	for hash, receipt := range receipts {
		receipts1[hash] = receipt
		receipts2[hash] = receipt
	}
	return hashes1, hashes2, headers1, headers2, blocks1, blocks2, receipts1, receipts2
}

func (dl *downloadTester) terminate() {
	dl.downloader.Terminate()
}

func (dl *downloadTester) sync(id string, td *big.Int, mode SyncMode) error {
	dl.lock.RLock()
	hash := dl.peerHashes[id][0]
	if td == nil {
		td = big.NewInt(1)
		if diff, ok := dl.peerChainTds[id][hash]; ok {
			td = diff
		}
	}
	dl.lock.RUnlock()

	err := dl.downloader.synchronise(id, hash, td, mode)
	select {
	case <-dl.downloader.cancelCh:
	default:
		panic("downloader active post sync cycle")
	}
	return err
}

func (dl *downloadTester) HasHeader(hash common.Hash, number uint64) bool {
	return dl.GetHeaderByHash(hash) != nil
}

func (dl *downloadTester) HasBlock(hash common.Hash, number uint64) bool {
	return dl.GetBlockByHash(hash) != nil
}

func (dl *downloadTester) GetHeaderByHash(hash common.Hash) *types.Header {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownHeaders[hash]
}

func (dl *downloadTester) GetBlockByHash(hash common.Hash) *types.Block {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownBlocks[hash]
}

func (dl *downloadTester) CurrentHeader() *types.Header {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if header := dl.ownHeaders[dl.ownHashes[i]]; header != nil {
			return header
		}
	}
	return dl.genesis.Header()
}

func (dl *downloadTester) CurrentBlock() *types.Block {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if block := dl.ownBlocks[dl.ownHashes[i]]; block != nil {
			if _, err := dl.stateDb.Get(block.Root().Bytes()); err == nil {
				return block
			}
		}
	}
	return dl.genesis
}

func (dl *downloadTester) CurrentFastBlock() *types.Block {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if block := dl.ownBlocks[dl.ownHashes[i]]; block != nil {
			return block
		}
	}
	return dl.genesis
}

func (dl *downloadTester) FastSyncCommitHead(hash common.Hash) error {
	if block := dl.GetBlockByHash(hash); block != nil {
		_, err := trie.NewSecure(block.Root(), trie.NewDatabase(dl.stateDb), 0)
		return err
	}
	return fmt.Errorf("non existent block: %x", hash[:4])
}

func (dl *downloadTester) GetTd(hash common.Hash, number uint64) *big.Int {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownChainTd[hash]
}

func (dl *downloadTester) InsertHeaderChain(headers []*types.Header, checkFreq int) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	if _, ok := dl.ownHeaders[headers[0].ParentHash]; !ok {
		return 0, errors.New("unknown parent")
	}
	for i := 1; i < len(headers); i++ {
		if headers[i].ParentHash != headers[i-1].Hash() {
			return i, errors.New("unknown parent")
		}
	}
	for i, header := range headers {
		if _, ok := dl.ownHeaders[header.Hash()]; ok {
			continue
		}
		if _, ok := dl.ownHeaders[header.ParentHash]; !ok {
			return i, errors.New("unknown parent")
		}
		dl.ownHashes = append(dl.ownHashes, header.Hash())
		dl.ownHeaders[header.Hash()] = header
		dl.ownChainTd[header.Hash()] = new(big.Int).Add(dl.ownChainTd[header.ParentHash], header.Difficulty)
	}
	return len(headers), nil
}

func (dl *downloadTester) InsertChain(blocks types.Blocks) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	for i, block := range blocks {
		if parent, ok := dl.ownBlocks[block.ParentHash()]; !ok {
			return i, errors.New("unknown parent")
		} else if _, err := dl.stateDb.Get(parent.Root().Bytes()); err != nil {
			return i, fmt.Errorf("unknown parent state %x: %v", parent.Root(), err)
		}
		if _, ok := dl.ownHeaders[block.Hash()]; !ok {
			dl.ownHashes = append(dl.ownHashes, block.Hash())
			dl.ownHeaders[block.Hash()] = block.Header()
		}
		dl.ownBlocks[block.Hash()] = block
		dl.stateDb.Put(block.Root().Bytes(), []byte{0x00})
		dl.ownChainTd[block.Hash()] = new(big.Int).Add(dl.ownChainTd[block.ParentHash()], block.Difficulty())
	}
	return len(blocks), nil
}

func (dl *downloadTester) InsertReceiptChain(blocks types.Blocks, receipts []types.Receipts) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	for i := 0; i < len(blocks) && i < len(receipts); i++ {
		if _, ok := dl.ownHeaders[blocks[i].Hash()]; !ok {
			return i, errors.New("unknown owner")
		}
		if _, ok := dl.ownBlocks[blocks[i].ParentHash()]; !ok {
			return i, errors.New("unknown parent")
		}
		dl.ownBlocks[blocks[i].Hash()] = blocks[i]
		dl.ownReceipts[blocks[i].Hash()] = receipts[i]
	}
	return len(blocks), nil
}

func (dl *downloadTester) Rollback(hashes []common.Hash) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	for i := len(hashes) - 1; i >= 0; i-- {
		if dl.ownHashes[len(dl.ownHashes)-1] == hashes[i] {
			dl.ownHashes = dl.ownHashes[:len(dl.ownHashes)-1]
		}
		delete(dl.ownChainTd, hashes[i])
		delete(dl.ownHeaders, hashes[i])
		delete(dl.ownReceipts, hashes[i])
		delete(dl.ownBlocks, hashes[i])
	}
}

func (dl *downloadTester) newPeer(id string, version int, hashes []common.Hash, headers map[common.Hash]*types.Header, blocks map[common.Hash]*types.Block, receipts map[common.Hash]types.Receipts) error {
	return dl.newSlowPeer(id, version, hashes, headers, blocks, receipts, 0)
}

func (dl *downloadTester) newSlowPeer(id string, version int, hashes []common.Hash, headers map[common.Hash]*types.Header, blocks map[common.Hash]*types.Block, receipts map[common.Hash]types.Receipts, delay time.Duration) error {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	var err = dl.downloader.RegisterPeer(id, version, &downloadTesterPeer{dl: dl, id: id, delay: delay})
	if err == nil {
		dl.peerHashes[id] = make([]common.Hash, len(hashes))
		copy(dl.peerHashes[id], hashes)

		dl.peerHeaders[id] = make(map[common.Hash]*types.Header)
		dl.peerBlocks[id] = make(map[common.Hash]*types.Block)
		dl.peerReceipts[id] = make(map[common.Hash]types.Receipts)
		dl.peerChainTds[id] = make(map[common.Hash]*big.Int)
		dl.peerMissingStates[id] = make(map[common.Hash]bool)

		genesis := hashes[len(hashes)-1]
		if header := headers[genesis]; header != nil {
			dl.peerHeaders[id][genesis] = header
			dl.peerChainTds[id][genesis] = header.Difficulty
		}
		if block := blocks[genesis]; block != nil {
			dl.peerBlocks[id][genesis] = block
			dl.peerChainTds[id][genesis] = block.Difficulty()
		}

		for i := len(hashes) - 2; i >= 0; i-- {
			hash := hashes[i]

			if header, ok := headers[hash]; ok {
				dl.peerHeaders[id][hash] = header
				if _, ok := dl.peerHeaders[id][header.ParentHash]; ok {
					dl.peerChainTds[id][hash] = new(big.Int).Add(header.Difficulty, dl.peerChainTds[id][header.ParentHash])
				}
			}
			if block, ok := blocks[hash]; ok {
				dl.peerBlocks[id][hash] = block
				if _, ok := dl.peerBlocks[id][block.ParentHash()]; ok {
					dl.peerChainTds[id][hash] = new(big.Int).Add(block.Difficulty(), dl.peerChainTds[id][block.ParentHash()])
				}
			}
			if receipt, ok := receipts[hash]; ok {
				dl.peerReceipts[id][hash] = receipt
			}
		}
	}
	return err
}

func (dl *downloadTester) dropPeer(id string) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	delete(dl.peerHashes, id)
	delete(dl.peerHeaders, id)
	delete(dl.peerBlocks, id)
	delete(dl.peerChainTds, id)

	dl.downloader.UnregisterPeer(id)
}

type downloadTesterPeer struct {
	dl    *downloadTester
	id    string
	delay time.Duration
	lock  sync.RWMutex
}

func (dlp *downloadTesterPeer) setDelay(delay time.Duration) {
	dlp.lock.Lock()
	defer dlp.lock.Unlock()

	dlp.delay = delay
}

func (dlp *downloadTesterPeer) waitDelay() {
	dlp.lock.RLock()
	delay := dlp.delay
	dlp.lock.RUnlock()

	time.Sleep(delay)
}

func (dlp *downloadTesterPeer) Head() (common.Hash, *big.Int) {
	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	return dlp.dl.peerHashes[dlp.id][0], nil
}

func (dlp *downloadTesterPeer) RequestHeadersByHash(origin common.Hash, amount int, skip int, reverse bool) error {
	dlp.dl.lock.RLock()
	number := uint64(0)
	for num, hash := range dlp.dl.peerHashes[dlp.id] {
		if hash == origin {
			number = uint64(len(dlp.dl.peerHashes[dlp.id]) - num - 1)
			break
		}
	}
	dlp.dl.lock.RUnlock()

	return dlp.RequestHeadersByNumber(number, amount, skip, reverse)
}

func (dlp *downloadTesterPeer) RequestHeadersByNumber(origin uint64, amount int, skip int, reverse bool) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	hashes := dlp.dl.peerHashes[dlp.id]
	headers := dlp.dl.peerHeaders[dlp.id]
	result := make([]*types.Header, 0, amount)
	for i := 0; i < amount && len(hashes)-int(origin)-1-i*(skip+1) >= 0; i++ {
		if header, ok := headers[hashes[len(hashes)-int(origin)-1-i*(skip+1)]]; ok {
			result = append(result, header)
		}
	}
	go func() {
		time.Sleep(time.Millisecond)
		dlp.dl.downloader.DeliverHeaders(dlp.id, result)
	}()
	return nil
}

func (dlp *downloadTesterPeer) RequestBodies(hashes []common.Hash) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	blocks := dlp.dl.peerBlocks[dlp.id]

	transactions := make([][]*types.Transaction, 0, len(hashes))
	uncles := make([][]*types.Header, 0, len(hashes))

	for _, hash := range hashes {
		if block, ok := blocks[hash]; ok {
			transactions = append(transactions, block.Transactions())
			uncles = append(uncles, block.Uncles())
		}
	}
	go dlp.dl.downloader.DeliverBodies(dlp.id, transactions, uncles)

	return nil
}

func (dlp *downloadTesterPeer) RequestReceipts(hashes []common.Hash) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	receipts := dlp.dl.peerReceipts[dlp.id]

	results := make([][]*types.Receipt, 0, len(hashes))
	for _, hash := range hashes {
		if receipt, ok := receipts[hash]; ok {
			results = append(results, receipt)
		}
	}
	go dlp.dl.downloader.DeliverReceipts(dlp.id, results)

	return nil
}

func (dlp *downloadTesterPeer) RequestNodeData(hashes []common.Hash) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	results := make([][]byte, 0, len(hashes))
	for _, hash := range hashes {
		if data, err := dlp.dl.peerDb.Get(hash.Bytes()); err == nil {
			if !dlp.dl.peerMissingStates[dlp.id][hash] {
				results = append(results, data)
			}
		}
	}
	go dlp.dl.downloader.DeliverNodeData(dlp.id, results)

	return nil
}

func assertOwnChain(t *testing.T, tester *downloadTester, length int) {
	assertOwnForkedChain(t, tester, 1, []int{length})
}

func assertOwnForkedChain(t *testing.T, tester *downloadTester, common int, lengths []int) {
	headers, blocks, receipts := lengths[0], lengths[0], lengths[0]-fsMinFullBlocks

	if receipts < 0 {
		receipts = 1
	}
	for _, length := range lengths[1:] {
		headers += length - common
		blocks += length - common
		receipts += length - common - fsMinFullBlocks
	}
	switch tester.downloader.mode {
	case FullSync:
		receipts = 1
	case LightSync:
		blocks, receipts = 1, 1
	}
	if hs := len(tester.ownHeaders); hs != headers {
		t.Fatalf("synchronised headers mismatch: have %v, want %v", hs, headers)
	}
	if bs := len(tester.ownBlocks); bs != blocks {
		t.Fatalf("synchronised blocks mismatch: have %v, want %v", bs, blocks)
	}
	if rs := len(tester.ownReceipts); rs != receipts {
		t.Fatalf("synchronised receipts mismatch: have %v, want %v", rs, receipts)
	}
	/*if tester.downloader.mode == FastSync {
		pivot := uint64(0)
		var index int
		if pivot := int(tester.downloader.queue.fastSyncPivot); pivot < common {
			index = pivot
		} else {
			index = len(tester.ownHashes) - lengths[len(lengths)-1] + int(tester.downloader.queue.fastSyncPivot)
		}
		if index > 0 {
			if statedb, err := state.New(tester.ownHeaders[tester.ownHashes[index]].Root, state.NewDatabase(trie.NewDatabase(tester.stateDb))); statedb == nil || err != nil {
				t.Fatalf("state reconstruction failed: %v", err)
			}
		}
	}*/
}

func TestCanonicalSynchronisation62(t *testing.T)      { testCanonicalSynchronisation(t, 62, FullSync) }
func TestCanonicalSynchronisation63Full(t *testing.T)  { testCanonicalSynchronisation(t, 63, FullSync) }
func TestCanonicalSynchronisation63Fast(t *testing.T)  { testCanonicalSynchronisation(t, 63, FastSync) }
func TestCanonicalSynchronisation64Full(t *testing.T)  { testCanonicalSynchronisation(t, 64, FullSync) }
func TestCanonicalSynchronisation64Fast(t *testing.T)  { testCanonicalSynchronisation(t, 64, FastSync) }
func TestCanonicalSynchronisation64Light(t *testing.T) { testCanonicalSynchronisation(t, 64, LightSync) }

func testCanonicalSynchronisation(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("peer", protocol, hashes, headers, blocks, receipts)

	if err := tester.sync("peer", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)
}

func TestThrottling62(t *testing.T)     { testThrottling(t, 62, FullSync) }
func TestThrottling63Full(t *testing.T) { testThrottling(t, 63, FullSync) }
func TestThrottling63Fast(t *testing.T) { testThrottling(t, 63, FastSync) }
func TestThrottling64Full(t *testing.T) { testThrottling(t, 64, FullSync) }
func TestThrottling64Fast(t *testing.T) { testThrottling(t, 64, FastSync) }

func testThrottling(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()
	tester := newTester()
	defer tester.terminate()

	targetBlocks := 8 * blockCacheItems
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("peer", protocol, hashes, headers, blocks, receipts)

	blocked, proceed := uint32(0), make(chan struct{})
	tester.downloader.chainInsertHook = func(results []*fetchResult) {
		atomic.StoreUint32(&blocked, uint32(len(results)))
		<-proceed
	}
	errc := make(chan error)
	go func() {
		errc <- tester.sync("peer", nil, mode)
	}()
	for {
		tester.lock.RLock()
		retrieved := len(tester.ownBlocks)
		tester.lock.RUnlock()
		if retrieved >= targetBlocks+1 {
			break
		}
		var cached, frozen int
		for start := time.Now(); time.Since(start) < 3*time.Second; {
			time.Sleep(25 * time.Millisecond)

			tester.lock.Lock()
			tester.downloader.queue.lock.Lock()
			cached = len(tester.downloader.queue.blockDonePool)
			if mode == FastSync {
				if receipts := len(tester.downloader.queue.receiptDonePool); receipts < cached {
					cached = receipts
				}
			}
			frozen = int(atomic.LoadUint32(&blocked))
			retrieved = len(tester.ownBlocks)
			tester.downloader.queue.lock.Unlock()
			tester.lock.Unlock()

			if cached == blockCacheItems || cached == blockCacheItems-reorgProtHeaderDelay || retrieved+cached+frozen == targetBlocks+1 || retrieved+cached+frozen == targetBlocks+1-reorgProtHeaderDelay {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)

		tester.lock.RLock()
		retrieved = len(tester.ownBlocks)
		tester.lock.RUnlock()
		if cached != blockCacheItems && cached != blockCacheItems-reorgProtHeaderDelay && retrieved+cached+frozen != targetBlocks+1 && retrieved+cached+frozen != targetBlocks+1-reorgProtHeaderDelay {
			t.Fatalf("block count mismatch: have %v, want %v (owned %v, blocked %v, target %v)", cached, blockCacheItems, retrieved, frozen, targetBlocks+1)
		}
		if atomic.LoadUint32(&blocked) > 0 {
			atomic.StoreUint32(&blocked, uint32(0))
			proceed <- struct{}{}
		}
	}
	assertOwnChain(t, tester, targetBlocks+1)
	if err := <-errc; err != nil {
		t.Fatalf("block synchronization failed: %v", err)
	}
}

func TestForkedSync62(t *testing.T)      { testForkedSync(t, 62, FullSync) }
func TestForkedSync63Full(t *testing.T)  { testForkedSync(t, 63, FullSync) }
func TestForkedSync63Fast(t *testing.T)  { testForkedSync(t, 63, FastSync) }
func TestForkedSync64Full(t *testing.T)  { testForkedSync(t, 64, FullSync) }
func TestForkedSync64Fast(t *testing.T)  { testForkedSync(t, 64, FastSync) }
func TestForkedSync64Light(t *testing.T) { testForkedSync(t, 64, LightSync) }

func testForkedSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	common, fork := MaxHashFetch, 2*MaxHashFetch
	hashesA, hashesB, headersA, headersB, blocksA, blocksB, receiptsA, receiptsB := tester.makeChainFork(common+fork, fork, tester.genesis, nil, true)

	tester.newPeer("fork A", protocol, hashesA, headersA, blocksA, receiptsA)
	tester.newPeer("fork B", protocol, hashesB, headersB, blocksB, receiptsB)

	if err := tester.sync("fork A", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, common+fork+1)

	if err := tester.sync("fork B", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnForkedChain(t, tester, common+1, []int{common + fork + 1, common + fork + 1})
}

func TestHeavyForkedSync62(t *testing.T)      { testHeavyForkedSync(t, 62, FullSync) }
func TestHeavyForkedSync63Full(t *testing.T)  { testHeavyForkedSync(t, 63, FullSync) }
func TestHeavyForkedSync63Fast(t *testing.T)  { testHeavyForkedSync(t, 63, FastSync) }
func TestHeavyForkedSync64Full(t *testing.T)  { testHeavyForkedSync(t, 64, FullSync) }
func TestHeavyForkedSync64Fast(t *testing.T)  { testHeavyForkedSync(t, 64, FastSync) }
func TestHeavyForkedSync64Light(t *testing.T) { testHeavyForkedSync(t, 64, LightSync) }

func testHeavyForkedSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	common, fork := MaxHashFetch, 4*MaxHashFetch
	hashesA, hashesB, headersA, headersB, blocksA, blocksB, receiptsA, receiptsB := tester.makeChainFork(common+fork, fork, tester.genesis, nil, false)

	tester.newPeer("light", protocol, hashesA, headersA, blocksA, receiptsA)
	tester.newPeer("heavy", protocol, hashesB[fork/2:], headersB, blocksB, receiptsB)

	if err := tester.sync("light", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, common+fork+1)

	if err := tester.sync("heavy", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnForkedChain(t, tester, common+1, []int{common + fork + 1, common + fork/2 + 1})
}

func TestBoundedForkedSync62(t *testing.T)      { testBoundedForkedSync(t, 62, FullSync) }
func TestBoundedForkedSync63Full(t *testing.T)  { testBoundedForkedSync(t, 63, FullSync) }
func TestBoundedForkedSync63Fast(t *testing.T)  { testBoundedForkedSync(t, 63, FastSync) }
func TestBoundedForkedSync64Full(t *testing.T)  { testBoundedForkedSync(t, 64, FullSync) }
func TestBoundedForkedSync64Fast(t *testing.T)  { testBoundedForkedSync(t, 64, FastSync) }
func TestBoundedForkedSync64Light(t *testing.T) { testBoundedForkedSync(t, 64, LightSync) }

func testBoundedForkedSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	common, fork := 13, int(MaxForkAncestry+17)
	hashesA, hashesB, headersA, headersB, blocksA, blocksB, receiptsA, receiptsB := tester.makeChainFork(common+fork, fork, tester.genesis, nil, true)

	tester.newPeer("original", protocol, hashesA, headersA, blocksA, receiptsA)
	tester.newPeer("rewriter", protocol, hashesB, headersB, blocksB, receiptsB)

	if err := tester.sync("original", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, common+fork+1)

	if err := tester.sync("rewriter", nil, mode); err != errInvalidAncestor {
		t.Fatalf("sync failure mismatch: have %v, want %v", err, errInvalidAncestor)
	}
}

func TestBoundedHeavyForkedSync62(t *testing.T)      { testBoundedHeavyForkedSync(t, 62, FullSync) }
func TestBoundedHeavyForkedSync63Full(t *testing.T)  { testBoundedHeavyForkedSync(t, 63, FullSync) }
func TestBoundedHeavyForkedSync63Fast(t *testing.T)  { testBoundedHeavyForkedSync(t, 63, FastSync) }
func TestBoundedHeavyForkedSync64Full(t *testing.T)  { testBoundedHeavyForkedSync(t, 64, FullSync) }
func TestBoundedHeavyForkedSync64Fast(t *testing.T)  { testBoundedHeavyForkedSync(t, 64, FastSync) }
func TestBoundedHeavyForkedSync64Light(t *testing.T) { testBoundedHeavyForkedSync(t, 64, LightSync) }

func testBoundedHeavyForkedSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	common, fork := 13, int(MaxForkAncestry+17)
	hashesA, hashesB, headersA, headersB, blocksA, blocksB, receiptsA, receiptsB := tester.makeChainFork(common+fork, fork, tester.genesis, nil, false)

	tester.newPeer("original", protocol, hashesA, headersA, blocksA, receiptsA)
	tester.newPeer("heavy-rewriter", protocol, hashesB[MaxForkAncestry-17:], headersB, blocksB, receiptsB)

	if err := tester.sync("original", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, common+fork+1)

	if err := tester.sync("heavy-rewriter", nil, mode); err != errInvalidAncestor {
		t.Fatalf("sync failure mismatch: have %v, want %v", err, errInvalidAncestor)
	}
}

func TestInactiveDownloader62(t *testing.T) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	if err := tester.downloader.DeliverHeaders("bad peer", []*types.Header{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
	if err := tester.downloader.DeliverBodies("bad peer", [][]*types.Transaction{}, [][]*types.Header{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
}

func TestInactiveDownloader63(t *testing.T) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	if err := tester.downloader.DeliverHeaders("bad peer", []*types.Header{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
	if err := tester.downloader.DeliverBodies("bad peer", [][]*types.Transaction{}, [][]*types.Header{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
	if err := tester.downloader.DeliverReceipts("bad peer", [][]*types.Receipt{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
}

func TestCancel62(t *testing.T)      { testCancel(t, 62, FullSync) }
func TestCancel63Full(t *testing.T)  { testCancel(t, 63, FullSync) }
func TestCancel63Fast(t *testing.T)  { testCancel(t, 63, FastSync) }
func TestCancel64Full(t *testing.T)  { testCancel(t, 64, FullSync) }
func TestCancel64Fast(t *testing.T)  { testCancel(t, 64, FastSync) }
func TestCancel64Light(t *testing.T) { testCancel(t, 64, LightSync) }

func testCancel(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	if targetBlocks >= MaxHashFetch {
		targetBlocks = MaxHashFetch - 15
	}
	if targetBlocks >= MaxHeaderFetch {
		targetBlocks = MaxHeaderFetch - 15
	}
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("peer", protocol, hashes, headers, blocks, receipts)

	tester.downloader.Cancel()
	if !tester.downloader.queue.Idle() {
		t.Errorf("download queue not idle")
	}
	if err := tester.sync("peer", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	tester.downloader.Cancel()
	if !tester.downloader.queue.Idle() {
		t.Errorf("download queue not idle")
	}
}

func TestMultiSynchronisation62(t *testing.T)      { testMultiSynchronisation(t, 62, FullSync) }
func TestMultiSynchronisation63Full(t *testing.T)  { testMultiSynchronisation(t, 63, FullSync) }
func TestMultiSynchronisation63Fast(t *testing.T)  { testMultiSynchronisation(t, 63, FastSync) }
func TestMultiSynchronisation64Full(t *testing.T)  { testMultiSynchronisation(t, 64, FullSync) }
func TestMultiSynchronisation64Fast(t *testing.T)  { testMultiSynchronisation(t, 64, FastSync) }
func TestMultiSynchronisation64Light(t *testing.T) { testMultiSynchronisation(t, 64, LightSync) }

func testMultiSynchronisation(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetPeers := 8
	targetBlocks := targetPeers*blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	for i := 0; i < targetPeers; i++ {
		id := fmt.Sprintf("peer #%d", i)
		tester.newPeer(id, protocol, hashes[i*blockCacheItems:], headers, blocks, receipts)
	}
	if err := tester.sync("peer #0", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)
}

func TestMultiProtoSynchronisation62(t *testing.T)      { testMultiProtoSync(t, 62, FullSync) }
func TestMultiProtoSynchronisation63Full(t *testing.T)  { testMultiProtoSync(t, 63, FullSync) }
func TestMultiProtoSynchronisation63Fast(t *testing.T)  { testMultiProtoSync(t, 63, FastSync) }
func TestMultiProtoSynchronisation64Full(t *testing.T)  { testMultiProtoSync(t, 64, FullSync) }
func TestMultiProtoSynchronisation64Fast(t *testing.T)  { testMultiProtoSync(t, 64, FastSync) }
func TestMultiProtoSynchronisation64Light(t *testing.T) { testMultiProtoSync(t, 64, LightSync) }

func testMultiProtoSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("peer 62", 62, hashes, headers, blocks, nil)
	tester.newPeer("peer 63", 63, hashes, headers, blocks, receipts)
	tester.newPeer("peer 64", 64, hashes, headers, blocks, receipts)

	if err := tester.sync(fmt.Sprintf("peer %d", protocol), nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)

	for _, version := range []int{62, 63, 64} {
		peer := fmt.Sprintf("peer %d", version)
		if _, ok := tester.peerHashes[peer]; !ok {
			t.Errorf("%s dropped", peer)
		}
	}
}

func TestEmptyShortCircuit62(t *testing.T)      { testEmptyShortCircuit(t, 62, FullSync) }
func TestEmptyShortCircuit63Full(t *testing.T)  { testEmptyShortCircuit(t, 63, FullSync) }
func TestEmptyShortCircuit63Fast(t *testing.T)  { testEmptyShortCircuit(t, 63, FastSync) }
func TestEmptyShortCircuit64Full(t *testing.T)  { testEmptyShortCircuit(t, 64, FullSync) }
func TestEmptyShortCircuit64Fast(t *testing.T)  { testEmptyShortCircuit(t, 64, FastSync) }
func TestEmptyShortCircuit64Light(t *testing.T) { testEmptyShortCircuit(t, 64, LightSync) }

func testEmptyShortCircuit(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := 2*blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("peer", protocol, hashes, headers, blocks, receipts)

	bodiesHave, receiptsHave := int32(0), int32(0)
	tester.downloader.bodyFetchHook = func(headers []*types.Header) {
		atomic.AddInt32(&bodiesHave, int32(len(headers)))
	}
	tester.downloader.receiptFetchHook = func(headers []*types.Header) {
		atomic.AddInt32(&receiptsHave, int32(len(headers)))
	}
	if err := tester.sync("peer", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)

	bodiesNeeded, receiptsNeeded := 0, 0
	for _, block := range blocks {
		if mode != LightSync && block != tester.genesis && (len(block.Transactions()) > 0 || len(block.Uncles()) > 0) {
			bodiesNeeded++
		}
	}
	for _, receipt := range receipts {
		if mode == FastSync && len(receipt) > 0 {
			receiptsNeeded++
		}
	}
	if int(bodiesHave) != bodiesNeeded {
		t.Errorf("body retrieval count mismatch: have %v, want %v", bodiesHave, bodiesNeeded)
	}
	if int(receiptsHave) != receiptsNeeded {
		t.Errorf("receipt retrieval count mismatch: have %v, want %v", receiptsHave, receiptsNeeded)
	}
}

func TestMissingHeaderAttack62(t *testing.T)      { testMissingHeaderAttack(t, 62, FullSync) }
func TestMissingHeaderAttack63Full(t *testing.T)  { testMissingHeaderAttack(t, 63, FullSync) }
func TestMissingHeaderAttack63Fast(t *testing.T)  { testMissingHeaderAttack(t, 63, FastSync) }
func TestMissingHeaderAttack64Full(t *testing.T)  { testMissingHeaderAttack(t, 64, FullSync) }
func TestMissingHeaderAttack64Fast(t *testing.T)  { testMissingHeaderAttack(t, 64, FastSync) }
func TestMissingHeaderAttack64Light(t *testing.T) { testMissingHeaderAttack(t, 64, LightSync) }

func testMissingHeaderAttack(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("attack", protocol, hashes, headers, blocks, receipts)
	missing := targetBlocks / 2
	delete(tester.peerHeaders["attack"], hashes[missing])

	if err := tester.sync("attack", nil, mode); err == nil {
		t.Fatalf("succeeded attacker synchronisation")
	}
	tester.newPeer("valid", protocol, hashes, headers, blocks, receipts)
	if err := tester.sync("valid", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)
}

func TestShiftedHeaderAttack62(t *testing.T)      { testShiftedHeaderAttack(t, 62, FullSync) }
func TestShiftedHeaderAttack63Full(t *testing.T)  { testShiftedHeaderAttack(t, 63, FullSync) }
func TestShiftedHeaderAttack63Fast(t *testing.T)  { testShiftedHeaderAttack(t, 63, FastSync) }
func TestShiftedHeaderAttack64Full(t *testing.T)  { testShiftedHeaderAttack(t, 64, FullSync) }
func TestShiftedHeaderAttack64Fast(t *testing.T)  { testShiftedHeaderAttack(t, 64, FastSync) }
func TestShiftedHeaderAttack64Light(t *testing.T) { testShiftedHeaderAttack(t, 64, LightSync) }

func testShiftedHeaderAttack(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("attack", protocol, hashes, headers, blocks, receipts)
	delete(tester.peerHeaders["attack"], hashes[len(hashes)-2])
	delete(tester.peerBlocks["attack"], hashes[len(hashes)-2])
	delete(tester.peerReceipts["attack"], hashes[len(hashes)-2])

	if err := tester.sync("attack", nil, mode); err == nil {
		t.Fatalf("succeeded attacker synchronisation")
	}
	tester.newPeer("valid", protocol, hashes, headers, blocks, receipts)
	if err := tester.sync("valid", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)
}

func TestInvalidHeaderRollback63Fast(t *testing.T)  { testInvalidHeaderRollback(t, 63, FastSync) }
func TestInvalidHeaderRollback64Fast(t *testing.T)  { testInvalidHeaderRollback(t, 64, FastSync) }
func TestInvalidHeaderRollback64Light(t *testing.T) { testInvalidHeaderRollback(t, 64, LightSync) }

func testInvalidHeaderRollback(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := 3*fsHeaderSafetyNet + 256 + fsMinFullBlocks
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	tester.newPeer("fast-attack", protocol, hashes, headers, blocks, receipts)
	missing := fsHeaderSafetyNet + MaxHeaderFetch + 1
	delete(tester.peerHeaders["fast-attack"], hashes[len(hashes)-missing])

	if err := tester.sync("fast-attack", nil, mode); err == nil {
		t.Fatalf("succeeded fast attacker synchronisation")
	}
	if head := tester.CurrentHeader().Number.Int64(); int(head) > MaxHeaderFetch {
		t.Errorf("rollback head mismatch: have %v, want at most %v", head, MaxHeaderFetch)
	}
	tester.newPeer("block-attack", protocol, hashes, headers, blocks, receipts)
	missing = 3*fsHeaderSafetyNet + MaxHeaderFetch + 1
	delete(tester.peerHeaders["fast-attack"], hashes[len(hashes)-missing])
	delete(tester.peerHeaders["block-attack"], hashes[len(hashes)-missing])

	if err := tester.sync("block-attack", nil, mode); err == nil {
		t.Fatalf("succeeded block attacker synchronisation")
	}
	if head := tester.CurrentHeader().Number.Int64(); int(head) > 2*fsHeaderSafetyNet+MaxHeaderFetch {
		t.Errorf("rollback head mismatch: have %v, want at most %v", head, 2*fsHeaderSafetyNet+MaxHeaderFetch)
	}
	if mode == FastSync {
		if head := tester.CurrentBlock().NumberU64(); head != 0 {
			t.Errorf("fast sync pivot block #%d not rolled back", head)
		}
	}
	tester.newPeer("withhold-attack", protocol, hashes, headers, blocks, receipts)
	missing = 3*fsHeaderSafetyNet + MaxHeaderFetch + 1

	tester.downloader.syncInitHook = func(uint64, uint64) {
		for i := missing; i <= len(hashes); i++ {
			delete(tester.peerHeaders["withhold-attack"], hashes[len(hashes)-i])
		}
		tester.downloader.syncInitHook = nil
	}

	if err := tester.sync("withhold-attack", nil, mode); err == nil {
		t.Fatalf("succeeded withholding attacker synchronisation")
	}
	if head := tester.CurrentHeader().Number.Int64(); int(head) > 2*fsHeaderSafetyNet+MaxHeaderFetch {
		t.Errorf("rollback head mismatch: have %v, want at most %v", head, 2*fsHeaderSafetyNet+MaxHeaderFetch)
	}
	if mode == FastSync {
		if head := tester.CurrentBlock().NumberU64(); head != 0 {
			t.Errorf("fast sync pivot block #%d not rolled back", head)
		}
	}
	tester.newPeer("valid", protocol, hashes, headers, blocks, receipts)
	if err := tester.sync("valid", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	if hs := len(tester.ownHeaders); hs != len(headers) {
		t.Fatalf("synchronised headers mismatch: have %v, want %v", hs, len(headers))
	}
	if mode != LightSync {
		if bs := len(tester.ownBlocks); bs != len(blocks) {
			t.Fatalf("synchronised blocks mismatch: have %v, want %v", bs, len(blocks))
		}
	}
}

func TestHighTDStarvationAttack62(t *testing.T)      { testHighTDStarvationAttack(t, 62, FullSync) }
func TestHighTDStarvationAttack63Full(t *testing.T)  { testHighTDStarvationAttack(t, 63, FullSync) }
func TestHighTDStarvationAttack63Fast(t *testing.T)  { testHighTDStarvationAttack(t, 63, FastSync) }
func TestHighTDStarvationAttack64Full(t *testing.T)  { testHighTDStarvationAttack(t, 64, FullSync) }
func TestHighTDStarvationAttack64Fast(t *testing.T)  { testHighTDStarvationAttack(t, 64, FastSync) }
func TestHighTDStarvationAttack64Light(t *testing.T) { testHighTDStarvationAttack(t, 64, LightSync) }

func testHighTDStarvationAttack(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	hashes, headers, blocks, receipts := tester.makeChain(0, 0, tester.genesis, nil, false)
	tester.newPeer("attack", protocol, []common.Hash{hashes[0]}, headers, blocks, receipts)

	if err := tester.sync("attack", big.NewInt(1000000), mode); err != errStallingPeer {
		t.Fatalf("synchronisation error mismatch: have %v, want %v", err, errStallingPeer)
	}
}

func TestBlockHeaderAttackerDropping62(t *testing.T) { testBlockHeaderAttackerDropping(t, 62) }
func TestBlockHeaderAttackerDropping63(t *testing.T) { testBlockHeaderAttackerDropping(t, 63) }
func TestBlockHeaderAttackerDropping64(t *testing.T) { testBlockHeaderAttackerDropping(t, 64) }

func testBlockHeaderAttackerDropping(t *testing.T, protocol int) {
	t.Parallel()

	tests := []struct {
		result error
		drop   bool
	}{
		{nil, false},
		{errBusy, false},
		{errUnknownPeer, false},
		{errBadPeer, true},
		{errStallingPeer, true},
		{errNoPeers, false},
		{errTimeout, true},
		{errEmptyHeaderSet, true},
		{errPeersUnavailable, true},
		{errInvalidAncestor, true},
		{errInvalidChain, true},
		{errInvalidBlock, false},
		{errInvalidBody, false},
		{errInvalidReceipt, false},
		{errCancelBlockFetch, false},
		{errCancelHeaderFetch, false},
		{errCancelBodyFetch, false},
		{errCancelReceiptFetch, false},
		{errCancelHeaderProcessing, false},
		{errCancelContentProcessing, false},
	}
	tester := newTester()
	defer tester.terminate()

	for i, tt := range tests {
		id := fmt.Sprintf("test %d", i)
		if err := tester.newPeer(id, protocol, []common.Hash{tester.genesis.Hash()}, nil, nil, nil); err != nil {
			t.Fatalf("test %d: failed to register new peer: %v", i, err)
		}
		if _, ok := tester.peerHashes[id]; !ok {
			t.Fatalf("test %d: registered peer not found", i)
		}
		tester.downloader.synchroniseMock = func(string, common.Hash) error { return tt.result }

		tester.downloader.Synchronise(id, tester.genesis.Hash(), big.NewInt(1000), FullSync)
		if _, ok := tester.peerHashes[id]; !ok != tt.drop {
			t.Errorf("test %d: peer drop mismatch for %v: have %v, want %v", i, tt.result, !ok, tt.drop)
		}
	}
}

func TestSyncProgress62(t *testing.T)      { testSyncProgress(t, 62, FullSync) }
func TestSyncProgress63Full(t *testing.T)  { testSyncProgress(t, 63, FullSync) }
func TestSyncProgress63Fast(t *testing.T)  { testSyncProgress(t, 63, FastSync) }
func TestSyncProgress64Full(t *testing.T)  { testSyncProgress(t, 64, FullSync) }
func TestSyncProgress64Fast(t *testing.T)  { testSyncProgress(t, 64, FastSync) }
func TestSyncProgress64Light(t *testing.T) { testSyncProgress(t, 64, LightSync) }

func testSyncProgress(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	starting := make(chan struct{})
	progress := make(chan struct{})

	tester.downloader.syncInitHook = func(origin, latest uint64) {
		starting <- struct{}{}
		<-progress
	}
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != 0 {
		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, 0)
	}
	tester.newPeer("peer-half", protocol, hashes[targetBlocks/2:], headers, blocks, receipts)
	pending := new(sync.WaitGroup)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("peer-half", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != uint64(targetBlocks/2+1) {
		t.Fatalf("Initial progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, targetBlocks/2+1)
	}
	progress <- struct{}{}
	pending.Wait()

	tester.newPeer("peer-full", protocol, hashes, headers, blocks, receipts)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("peer-full", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != uint64(targetBlocks/2+1) || progress.CurrentBlock != uint64(targetBlocks/2+1) || progress.HighestBlock != uint64(targetBlocks) {
		t.Fatalf("Completing progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, targetBlocks/2+1, targetBlocks/2+1, targetBlocks)
	}
	progress <- struct{}{}
	pending.Wait()

	if progress := tester.downloader.Progress(); progress.StartingBlock != uint64(targetBlocks/2+1) || progress.CurrentBlock != uint64(targetBlocks) || progress.HighestBlock != uint64(targetBlocks) {
		t.Fatalf("Final progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, targetBlocks/2+1, targetBlocks, targetBlocks)
	}
}

func TestForkedSyncProgress62(t *testing.T)      { testForkedSyncProgress(t, 62, FullSync) }
func TestForkedSyncProgress63Full(t *testing.T)  { testForkedSyncProgress(t, 63, FullSync) }
func TestForkedSyncProgress63Fast(t *testing.T)  { testForkedSyncProgress(t, 63, FastSync) }
func TestForkedSyncProgress64Full(t *testing.T)  { testForkedSyncProgress(t, 64, FullSync) }
func TestForkedSyncProgress64Fast(t *testing.T)  { testForkedSyncProgress(t, 64, FastSync) }
func TestForkedSyncProgress64Light(t *testing.T) { testForkedSyncProgress(t, 64, LightSync) }

func testForkedSyncProgress(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	common, fork := MaxHashFetch, 2*MaxHashFetch
	hashesA, hashesB, headersA, headersB, blocksA, blocksB, receiptsA, receiptsB := tester.makeChainFork(common+fork, fork, tester.genesis, nil, true)

	starting := make(chan struct{})
	progress := make(chan struct{})

	tester.downloader.syncInitHook = func(origin, latest uint64) {
		starting <- struct{}{}
		<-progress
	}
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != 0 {
		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, 0)
	}
	tester.newPeer("fork A", protocol, hashesA, headersA, blocksA, receiptsA)
	pending := new(sync.WaitGroup)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("fork A", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != uint64(len(hashesA)-1) {
		t.Fatalf("Initial progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, len(hashesA)-1)
	}
	progress <- struct{}{}
	pending.Wait()

	tester.downloader.syncStatsChainOrigin = tester.downloader.syncStatsChainHeight

	tester.newPeer("fork B", protocol, hashesB, headersB, blocksB, receiptsB)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("fork B", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != uint64(common) || progress.CurrentBlock != uint64(len(hashesA)-1) || progress.HighestBlock != uint64(len(hashesB)-1) {
		t.Fatalf("Forking progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, common, len(hashesA)-1, len(hashesB)-1)
	}
	progress <- struct{}{}
	pending.Wait()

	if progress := tester.downloader.Progress(); progress.StartingBlock != uint64(common) || progress.CurrentBlock != uint64(len(hashesB)-1) || progress.HighestBlock != uint64(len(hashesB)-1) {
		t.Fatalf("Final progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, common, len(hashesB)-1, len(hashesB)-1)
	}
}

func TestFailedSyncProgress62(t *testing.T)      { testFailedSyncProgress(t, 62, FullSync) }
func TestFailedSyncProgress63Full(t *testing.T)  { testFailedSyncProgress(t, 63, FullSync) }
func TestFailedSyncProgress63Fast(t *testing.T)  { testFailedSyncProgress(t, 63, FastSync) }
func TestFailedSyncProgress64Full(t *testing.T)  { testFailedSyncProgress(t, 64, FullSync) }
func TestFailedSyncProgress64Fast(t *testing.T)  { testFailedSyncProgress(t, 64, FastSync) }
func TestFailedSyncProgress64Light(t *testing.T) { testFailedSyncProgress(t, 64, LightSync) }

func testFailedSyncProgress(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks, 0, tester.genesis, nil, false)

	starting := make(chan struct{})
	progress := make(chan struct{})

	tester.downloader.syncInitHook = func(origin, latest uint64) {
		starting <- struct{}{}
		<-progress
	}
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != 0 {
		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, 0)
	}
	tester.newPeer("faulty", protocol, hashes, headers, blocks, receipts)
	missing := targetBlocks / 2
	delete(tester.peerHeaders["faulty"], hashes[missing])
	delete(tester.peerBlocks["faulty"], hashes[missing])
	delete(tester.peerReceipts["faulty"], hashes[missing])

	pending := new(sync.WaitGroup)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("faulty", nil, mode); err == nil {
			panic("succeeded faulty synchronisation")
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != uint64(targetBlocks) {
		t.Fatalf("Initial progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, targetBlocks)
	}
	progress <- struct{}{}
	pending.Wait()

	tester.newPeer("valid", protocol, hashes, headers, blocks, receipts)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("valid", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock > uint64(targetBlocks/2) || progress.HighestBlock != uint64(targetBlocks) {
		t.Fatalf("Completing progress mismatch: have %v/%v/%v, want %v/0-%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, targetBlocks/2, targetBlocks)
	}
	progress <- struct{}{}
	pending.Wait()

	if progress := tester.downloader.Progress(); progress.StartingBlock > uint64(targetBlocks/2) || progress.CurrentBlock != uint64(targetBlocks) || progress.HighestBlock != uint64(targetBlocks) {
		t.Fatalf("Final progress mismatch: have %v/%v/%v, want 0-%v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, targetBlocks/2, targetBlocks, targetBlocks)
	}
}

func TestFakedSyncProgress62(t *testing.T)      { testFakedSyncProgress(t, 62, FullSync) }
func TestFakedSyncProgress63Full(t *testing.T)  { testFakedSyncProgress(t, 63, FullSync) }
func TestFakedSyncProgress63Fast(t *testing.T)  { testFakedSyncProgress(t, 63, FastSync) }
func TestFakedSyncProgress64Full(t *testing.T)  { testFakedSyncProgress(t, 64, FullSync) }
func TestFakedSyncProgress64Fast(t *testing.T)  { testFakedSyncProgress(t, 64, FastSync) }
func TestFakedSyncProgress64Light(t *testing.T) { testFakedSyncProgress(t, 64, LightSync) }

func testFakedSyncProgress(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	targetBlocks := blockCacheItems - 15
	hashes, headers, blocks, receipts := tester.makeChain(targetBlocks+3, 0, tester.genesis, nil, false)

	starting := make(chan struct{})
	progress := make(chan struct{})

	tester.downloader.syncInitHook = func(origin, latest uint64) {
		starting <- struct{}{}
		<-progress
	}
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != 0 {
		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, 0)
	}
	tester.newPeer("attack", protocol, hashes, headers, blocks, receipts)
	for i := 1; i < 3; i++ {
		delete(tester.peerHeaders["attack"], hashes[i])
		delete(tester.peerBlocks["attack"], hashes[i])
		delete(tester.peerReceipts["attack"], hashes[i])
	}

	pending := new(sync.WaitGroup)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("attack", nil, mode); err == nil {
			panic("succeeded attacker synchronisation")
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock != 0 || progress.HighestBlock != uint64(targetBlocks+3) {
		t.Fatalf("Initial progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, 0, targetBlocks+3)
	}
	progress <- struct{}{}
	pending.Wait()

	tester.newPeer("valid", protocol, hashes[3:], headers, blocks, receipts)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("valid", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingBlock != 0 || progress.CurrentBlock > uint64(targetBlocks) || progress.HighestBlock != uint64(targetBlocks) {
		t.Fatalf("Completing progress mismatch: have %v/%v/%v, want %v/0-%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, 0, targetBlocks, targetBlocks)
	}
	progress <- struct{}{}
	pending.Wait()

	if progress := tester.downloader.Progress(); progress.StartingBlock > uint64(targetBlocks) || progress.CurrentBlock != uint64(targetBlocks) || progress.HighestBlock != uint64(targetBlocks) {
		t.Fatalf("Final progress mismatch: have %v/%v/%v, want 0-%v/%v/%v", progress.StartingBlock, progress.CurrentBlock, progress.HighestBlock, targetBlocks, targetBlocks, targetBlocks)
	}
}

func TestDeliverHeadersHang(t *testing.T) {
	testCases := []struct {
		protocol int
		syncMode SyncMode
	}{
		{62, FullSync},
		{63, FullSync},
		{63, FastSync},
		{64, FullSync},
		{64, FastSync},
		{64, LightSync},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("protocol %d mode %v", tc.protocol, tc.syncMode), func(t *testing.T) {
			testDeliverHeadersHang(t, tc.protocol, tc.syncMode)
		})
	}
}

type floodingTestPeer struct {
	peer   Peer
	tester *downloadTester
	pend   sync.WaitGroup
}

func (ftp *floodingTestPeer) Head() (common.Hash, *big.Int) { return ftp.peer.Head() }
func (ftp *floodingTestPeer) RequestHeadersByHash(hash common.Hash, count int, skip int, reverse bool) error {
	return ftp.peer.RequestHeadersByHash(hash, count, skip, reverse)
}
func (ftp *floodingTestPeer) RequestBodies(hashes []common.Hash) error {
	return ftp.peer.RequestBodies(hashes)
}
func (ftp *floodingTestPeer) RequestReceipts(hashes []common.Hash) error {
	return ftp.peer.RequestReceipts(hashes)
}
func (ftp *floodingTestPeer) RequestNodeData(hashes []common.Hash) error {
	return ftp.peer.RequestNodeData(hashes)
}

func (ftp *floodingTestPeer) RequestHeadersByNumber(from uint64, count, skip int, reverse bool) error {
	deliveriesDone := make(chan struct{}, 500)
	for i := 0; i < cap(deliveriesDone); i++ {
		peer := fmt.Sprintf("fake-peer%d", i)
		ftp.pend.Add(1)

		go func() {
			ftp.tester.downloader.DeliverHeaders(peer, []*types.Header{{}, {}, {}, {}})
			deliveriesDone <- struct{}{}
			ftp.pend.Done()
		}()
	}
	go ftp.peer.RequestHeadersByNumber(from, count, skip, reverse)
	timeout := time.After(60 * time.Second)
	for i := 0; i < cap(deliveriesDone); i++ {
		select {
		case <-deliveriesDone:
		case <-timeout:
			panic("blocked")
		}
	}
	return nil
}

func testDeliverHeadersHang(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	master := newTester()
	defer master.terminate()

	hashes, headers, blocks, receipts := master.makeChain(5, 0, master.genesis, nil, false)
	for i := 0; i < 200; i++ {
		tester := newTester()
		tester.peerDb = master.peerDb

		tester.newPeer("peer", protocol, hashes, headers, blocks, receipts)
		tester.downloader.peers.peers["peer"].peer = &floodingTestPeer{
			peer:   tester.downloader.peers.peers["peer"].peer,
			tester: tester,
		}
		if err := tester.sync("peer", nil, mode); err != nil {
			t.Errorf("test %d: sync failed: %v", i, err)
		}
		tester.terminate()

		tester.downloader.peers.peers["peer"].peer.(*floodingTestPeer).pend.Wait()
	}
}
