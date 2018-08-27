package filters

import (
	"context"
	"fmt"
	"math/big"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/bloombits"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/rpc"
)

type testBackend struct {
	mux        *event.TypeMux
	db         candb.Database
	sections   uint64
	txFeed     *event.Feed
	rmLogsFeed *event.Feed
	logsFeed   *event.Feed
	chainFeed  *event.Feed
}

func (b *testBackend) ChainDb() candb.Database {
	return b.db
}

func (b *testBackend) EventMux() *event.TypeMux {
	return b.mux
}

func (b *testBackend) HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error) {
	var (
		hash common.Hash
		num  uint64
	)
	if blockNr == rpc.LatestBlockNumber {
		hash = rawdb.ReadHeadBlockHash(b.db)
		number := rawdb.ReadHeaderNumber(b.db, hash)
		if number == nil {
			return nil, nil
		}
		num = *number
	} else {
		num = uint64(blockNr)
		hash = rawdb.ReadCanonicalHash(b.db, num)
	}
	return rawdb.ReadHeader(b.db, hash, num), nil
}

func (b *testBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	number := rawdb.ReadHeaderNumber(b.db, hash)
	if number == nil {
		return nil, nil
	}
	return rawdb.ReadHeader(b.db, hash, *number), nil
}

func (b *testBackend) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	if number := rawdb.ReadHeaderNumber(b.db, hash); number != nil {
		return rawdb.ReadReceipts(b.db, hash, *number), nil
	}
	return nil, nil
}

func (b *testBackend) GetLogs(ctx context.Context, hash common.Hash) ([][]*types.Log, error) {
	number := rawdb.ReadHeaderNumber(b.db, hash)
	if number == nil {
		return nil, nil
	}
	receipts := rawdb.ReadReceipts(b.db, hash, *number)

	logs := make([][]*types.Log, len(receipts))
	for i, receipt := range receipts {
		logs[i] = receipt.Logs
	}
	return logs, nil
}

func (b *testBackend) SubscribeNewTxsEvent(ch chan<- kernel.NewTxsEvent) event.Subscription {
	return b.txFeed.Subscribe(ch)
}

func (b *testBackend) SubscribeRemovedLogsEvent(ch chan<- kernel.RemovedLogsEvent) event.Subscription {
	return b.rmLogsFeed.Subscribe(ch)
}

func (b *testBackend) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return b.logsFeed.Subscribe(ch)
}

func (b *testBackend) SubscribeChainEvent(ch chan<- kernel.ChainEvent) event.Subscription {
	return b.chainFeed.Subscribe(ch)
}

func (b *testBackend) BloomStatus() (uint64, uint64) {
	return params.BloomBitsBlocks, b.sections
}

func (b *testBackend) ServiceFilter(ctx context.Context, session *bloombits.MatcherSession) {
	requests := make(chan chan *bloombits.Retrieval)

	go session.Multiplex(16, 0, requests)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return

			case request := <-requests:
				task := <-request

				task.Bitsets = make([][]byte, len(task.Sections))
				for i, section := range task.Sections {
					if rand.Int()%4 != 0 {
						head := rawdb.ReadCanonicalHash(b.db, (section+1)*params.BloomBitsBlocks-1)
						task.Bitsets[i], _ = rawdb.ReadBloomBits(b.db, task.Bit, section, head)
					}
				}
				request <- task
			}
		}
	}()
}

func TestBlockSubscription(t *testing.T) {
	t.Parallel()

	var (
		mux         = new(event.TypeMux)
		db          = candb.NewMemDatabase()
		txFeed      = new(event.Feed)
		rmLogsFeed  = new(event.Feed)
		logsFeed    = new(event.Feed)
		chainFeed   = new(event.Feed)
		backend     = &testBackend{mux, db, 0, txFeed, rmLogsFeed, logsFeed, chainFeed}
		api         = NewPublicFilterAPI(backend, false)
		genesis     = new(kernel.Genesis).MustCommit(db)
		chain, _    = kernel.GenerateChain(params.TestChainConfig, genesis, ethash.NewFaker(), db, 10, func(i int, gen *kernel.BlockGen) {})
		chainEvents = []kernel.ChainEvent{}
	)

	for _, blk := range chain {
		chainEvents = append(chainEvents, kernel.ChainEvent{Hash: blk.Hash(), Block: blk})
	}

	chan0 := make(chan *types.Header)
	sub0 := api.events.SubscribeNewHeads(chan0)
	chan1 := make(chan *types.Header)
	sub1 := api.events.SubscribeNewHeads(chan1)

	go func() {
		i1, i2 := 0, 0
		for i1 != len(chainEvents) || i2 != len(chainEvents) {
			select {
			case header := <-chan0:
				if chainEvents[i1].Hash != header.Hash() {
					t.Errorf("sub0 received invalid hash on index %d, want %x, got %x", i1, chainEvents[i1].Hash, header.Hash())
				}
				i1++
			case header := <-chan1:
				if chainEvents[i2].Hash != header.Hash() {
					t.Errorf("sub1 received invalid hash on index %d, want %x, got %x", i2, chainEvents[i2].Hash, header.Hash())
				}
				i2++
			}
		}

		sub0.Unsubscribe()
		sub1.Unsubscribe()
	}()

	time.Sleep(1 * time.Second)
	for _, e := range chainEvents {
		chainFeed.Send(e)
	}

	<-sub0.Err()
	<-sub1.Err()
}

func TestPendingTxFilter(t *testing.T) {
	t.Parallel()

	var (
		mux        = new(event.TypeMux)
		db         = candb.NewMemDatabase()
		txFeed     = new(event.Feed)
		rmLogsFeed = new(event.Feed)
		logsFeed   = new(event.Feed)
		chainFeed  = new(event.Feed)
		backend    = &testBackend{mux, db, 0, txFeed, rmLogsFeed, logsFeed, chainFeed}
		api        = NewPublicFilterAPI(backend, false)

		transactions = []*types.Transaction{
			types.NewTransaction(0, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(1, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(2, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(3, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
			types.NewTransaction(4, common.HexToAddress("0xb794f5ea0ba39494ce83a213fffba74279579268"), new(big.Int), 0, new(big.Int), nil),
		}

		hashes []common.Hash
	)

	fid0 := api.NewPendingTransactionFilter()

	time.Sleep(1 * time.Second)
	txFeed.Send(kernel.NewTxsEvent{Txs: transactions})

	timeout := time.Now().Add(1 * time.Second)
	for {
		results, err := api.GetFilterChanges(fid0)
		if err != nil {
			t.Fatalf("Unable to retrieve logs: %v", err)
		}

		h := results.([]common.Hash)
		hashes = append(hashes, h...)
		if len(hashes) >= len(transactions) {
			break
		}
		if time.Now().After(timeout) {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	if len(hashes) != len(transactions) {
		t.Errorf("invalid number of transactions, want %d transactions(s), got %d", len(transactions), len(hashes))
		return
	}
	for i := range hashes {
		if hashes[i] != transactions[i].Hash() {
			t.Errorf("hashes[%d] invalid, want %x, got %x", i, transactions[i].Hash(), hashes[i])
		}
	}
}

func TestLogFilterCreation(t *testing.T) {
	var (
		mux        = new(event.TypeMux)
		db         = candb.NewMemDatabase()
		txFeed     = new(event.Feed)
		rmLogsFeed = new(event.Feed)
		logsFeed   = new(event.Feed)
		chainFeed  = new(event.Feed)
		backend    = &testBackend{mux, db, 0, txFeed, rmLogsFeed, logsFeed, chainFeed}
		api        = NewPublicFilterAPI(backend, false)

		testCases = []struct {
			crit    FilterCriteria
			success bool
		}{
			{FilterCriteria{}, true},
			{FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(2)}, true},
			{FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, true},
			{FilterCriteria{FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64()), ToBlock: big.NewInt(rpc.PendingBlockNumber.Int64())}, true},
			{FilterCriteria{FromBlock: big.NewInt(2), ToBlock: big.NewInt(1)}, false},
			{FilterCriteria{FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64()), ToBlock: big.NewInt(100)}, false},
			{FilterCriteria{FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(100)}, false},
			{FilterCriteria{FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, false},
		}
	)

	for i, test := range testCases {
		_, err := api.NewFilter(test.crit)
		if test.success && err != nil {
			t.Errorf("expected filter creation for case %d to success, got %v", i, err)
		}
		if !test.success && err == nil {
			t.Errorf("expected testcase %d to fail with an error", i)
		}
	}
}

func TestInvalidLogFilterCreation(t *testing.T) {
	t.Parallel()

	var (
		mux        = new(event.TypeMux)
		db         = candb.NewMemDatabase()
		txFeed     = new(event.Feed)
		rmLogsFeed = new(event.Feed)
		logsFeed   = new(event.Feed)
		chainFeed  = new(event.Feed)
		backend    = &testBackend{mux, db, 0, txFeed, rmLogsFeed, logsFeed, chainFeed}
		api        = NewPublicFilterAPI(backend, false)
	)

	testCases := []FilterCriteria{
		0: {FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())},
		1: {FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(100)},
		2: {FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64()), ToBlock: big.NewInt(100)},
	}

	for i, test := range testCases {
		if _, err := api.NewFilter(test); err == nil {
			t.Errorf("Expected NewFilter for case #%d to fail", i)
		}
	}
}

func TestInvalidGetLogsRequest(t *testing.T) {
	var (
		mux        = new(event.TypeMux)
		db         = candb.NewMemDatabase()
		txFeed     = new(event.Feed)
		rmLogsFeed = new(event.Feed)
		logsFeed   = new(event.Feed)
		chainFeed  = new(event.Feed)
		backend    = &testBackend{mux, db, 0, txFeed, rmLogsFeed, logsFeed, chainFeed}
		api        = NewPublicFilterAPI(backend, false)
		blockHash  = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	)

	testCases := []FilterCriteria{
		0: {BlockHash: &blockHash, FromBlock: big.NewInt(100)},
		1: {BlockHash: &blockHash, ToBlock: big.NewInt(500)},
		2: {BlockHash: &blockHash, FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64())},
	}

	for i, test := range testCases {
		if _, err := api.GetLogs(context.Background(), test); err == nil {
			t.Errorf("Expected Logs for case #%d to fail", i)
		}
	}
}

func TestLogFilter(t *testing.T) {
	t.Parallel()

	var (
		mux        = new(event.TypeMux)
		db         = candb.NewMemDatabase()
		txFeed     = new(event.Feed)
		rmLogsFeed = new(event.Feed)
		logsFeed   = new(event.Feed)
		chainFeed  = new(event.Feed)
		backend    = &testBackend{mux, db, 0, txFeed, rmLogsFeed, logsFeed, chainFeed}
		api        = NewPublicFilterAPI(backend, false)

		firstAddr      = common.HexToAddress("0x1111111111111111111111111111111111111111")
		secondAddr     = common.HexToAddress("0x2222222222222222222222222222222222222222")
		thirdAddress   = common.HexToAddress("0x3333333333333333333333333333333333333333")
		notUsedAddress = common.HexToAddress("0x9999999999999999999999999999999999999999")
		firstTopic     = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
		secondTopic    = common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
		notUsedTopic   = common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")

		allLogs = []*types.Log{
			{Address: firstAddr},
			{Address: firstAddr, Topics: []common.Hash{firstTopic}, BlockNumber: 1},
			{Address: secondAddr, Topics: []common.Hash{firstTopic}, BlockNumber: 1},
			{Address: thirdAddress, Topics: []common.Hash{secondTopic}, BlockNumber: 2},
			{Address: thirdAddress, Topics: []common.Hash{secondTopic}, BlockNumber: 3},
		}

		expectedCase7  = []*types.Log{allLogs[3], allLogs[4], allLogs[0], allLogs[1], allLogs[2], allLogs[3], allLogs[4]}
		expectedCase11 = []*types.Log{allLogs[1], allLogs[2], allLogs[1], allLogs[2]}

		testCases = []struct {
			crit     FilterCriteria
			expected []*types.Log
			id       rpc.ID
		}{
			0:  {FilterCriteria{}, allLogs, ""},
			1:  {FilterCriteria{Addresses: []common.Address{{}, notUsedAddress}, Topics: [][]common.Hash{nil}}, []*types.Log{}, ""},
			2:  {FilterCriteria{Addresses: []common.Address{firstAddr}}, allLogs[:2], ""},
			3:  {FilterCriteria{Addresses: []common.Address{secondAddr}, Topics: [][]common.Hash{{notUsedTopic}}}, []*types.Log{}, ""},
			4:  {FilterCriteria{Addresses: []common.Address{thirdAddress}, Topics: [][]common.Hash{{firstTopic, secondTopic}}}, allLogs[3:5], ""},
			5:  {FilterCriteria{Addresses: []common.Address{secondAddr, thirdAddress}, Topics: [][]common.Hash{{firstTopic, secondTopic}}}, allLogs[2:5], ""},
			6:  {FilterCriteria{Addresses: []common.Address{firstAddr}, FromBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), ToBlock: big.NewInt(rpc.PendingBlockNumber.Int64())}, allLogs[:2], ""},
			7:  {FilterCriteria{FromBlock: big.NewInt(2), ToBlock: big.NewInt(rpc.PendingBlockNumber.Int64())}, expectedCase7, ""},
			8:  {FilterCriteria{FromBlock: big.NewInt(2), ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, allLogs[3:], ""},
			9:  {FilterCriteria{ToBlock: big.NewInt(rpc.LatestBlockNumber.Int64())}, allLogs, ""},
			10: {FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(2), Topics: [][]common.Hash{{secondTopic}}}, allLogs[3:4], ""},
			11: {FilterCriteria{FromBlock: big.NewInt(rpc.LatestBlockNumber.Int64()), ToBlock: big.NewInt(rpc.PendingBlockNumber.Int64()), Topics: [][]common.Hash{{firstTopic}}}, expectedCase11, ""},
			12: {FilterCriteria{Topics: [][]common.Hash{nil}}, allLogs[1:], ""},
		}
	)

	for i := range testCases {
		testCases[i].id, _ = api.NewFilter(testCases[i].crit)
	}

	time.Sleep(1 * time.Second)
	if nsend := logsFeed.Send(allLogs); nsend == 0 {
		t.Fatal("Shoud have at least one subscription")
	}
	if err := mux.Post(kernel.PendingLogsEvent{Logs: allLogs}); err != nil {
		t.Fatal(err)
	}

	for i, tt := range testCases {
		var fetched []*types.Log
		timeout := time.Now().Add(1 * time.Second)
		for {
			results, err := api.GetFilterChanges(tt.id)
			if err != nil {
				t.Fatalf("Unable to fetch logs: %v", err)
			}

			fetched = append(fetched, results.([]*types.Log)...)
			if len(fetched) >= len(tt.expected) {
				break
			}
			if time.Now().After(timeout) {
				break
			}

			time.Sleep(100 * time.Millisecond)
		}

		if len(fetched) != len(tt.expected) {
			t.Errorf("invalid number of logs for case %d, want %d log(s), got %d", i, len(tt.expected), len(fetched))
			return
		}

		for l := range fetched {
			if fetched[l].Removed {
				t.Errorf("expected log not to be removed for log %d in case %d", l, i)
			}
			if !reflect.DeepEqual(fetched[l], tt.expected[l]) {
				t.Errorf("invalid log on index %d for case %d", l, i)
			}
		}
	}
}

func TestPendingLogsSubscription(t *testing.T) {
	t.Parallel()

	var (
		mux        = new(event.TypeMux)
		db         = candb.NewMemDatabase()
		txFeed     = new(event.Feed)
		rmLogsFeed = new(event.Feed)
		logsFeed   = new(event.Feed)
		chainFeed  = new(event.Feed)
		backend    = &testBackend{mux, db, 0, txFeed, rmLogsFeed, logsFeed, chainFeed}
		api        = NewPublicFilterAPI(backend, false)

		firstAddr      = common.HexToAddress("0x1111111111111111111111111111111111111111")
		secondAddr     = common.HexToAddress("0x2222222222222222222222222222222222222222")
		thirdAddress   = common.HexToAddress("0x3333333333333333333333333333333333333333")
		notUsedAddress = common.HexToAddress("0x9999999999999999999999999999999999999999")
		firstTopic     = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
		secondTopic    = common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
		thirdTopic     = common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
		fourthTopic    = common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444")
		notUsedTopic   = common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")

		allLogs = []kernel.PendingLogsEvent{
			{Logs: []*types.Log{{Address: firstAddr, Topics: []common.Hash{}, BlockNumber: 0}}},
			{Logs: []*types.Log{{Address: firstAddr, Topics: []common.Hash{firstTopic}, BlockNumber: 1}}},
			{Logs: []*types.Log{{Address: secondAddr, Topics: []common.Hash{firstTopic}, BlockNumber: 2}}},
			{Logs: []*types.Log{{Address: thirdAddress, Topics: []common.Hash{secondTopic}, BlockNumber: 3}}},
			{Logs: []*types.Log{{Address: thirdAddress, Topics: []common.Hash{secondTopic}, BlockNumber: 4}}},
			{Logs: []*types.Log{
				{Address: thirdAddress, Topics: []common.Hash{firstTopic}, BlockNumber: 5},
				{Address: thirdAddress, Topics: []common.Hash{thirdTopic}, BlockNumber: 5},
				{Address: thirdAddress, Topics: []common.Hash{fourthTopic}, BlockNumber: 5},
				{Address: firstAddr, Topics: []common.Hash{firstTopic}, BlockNumber: 5},
			}},
		}

		convertLogs = func(pl []kernel.PendingLogsEvent) []*types.Log {
			var logs []*types.Log
			for _, l := range pl {
				logs = append(logs, l.Logs...)
			}
			return logs
		}

		testCases = []struct {
			crit     canchain.FilterQuery
			expected []*types.Log
			c        chan []*types.Log
			sub      *Subscription
		}{
			{canchain.FilterQuery{}, convertLogs(allLogs), nil, nil},
			{canchain.FilterQuery{Addresses: []common.Address{{}, notUsedAddress}, Topics: [][]common.Hash{nil}}, []*types.Log{}, nil, nil},
			{canchain.FilterQuery{Addresses: []common.Address{firstAddr}}, append(convertLogs(allLogs[:2]), allLogs[5].Logs[3]), nil, nil},
			{canchain.FilterQuery{Addresses: []common.Address{secondAddr}, Topics: [][]common.Hash{{notUsedTopic}}}, []*types.Log{}, nil, nil},
			{canchain.FilterQuery{Addresses: []common.Address{thirdAddress}, Topics: [][]common.Hash{{firstTopic, secondTopic}}}, append(convertLogs(allLogs[3:5]), allLogs[5].Logs[0]), nil, nil},
			{canchain.FilterQuery{Addresses: []common.Address{secondAddr, thirdAddress}, Topics: [][]common.Hash{{firstTopic, secondTopic}}}, append(convertLogs(allLogs[2:5]), allLogs[5].Logs[0]), nil, nil},
			{canchain.FilterQuery{Addresses: []common.Address{firstAddr}, FromBlock: big.NewInt(2), ToBlock: big.NewInt(3)}, append(convertLogs(allLogs[:2]), allLogs[5].Logs[3]), nil, nil},
			{canchain.FilterQuery{Addresses: []common.Address{thirdAddress}, Topics: [][]common.Hash{{firstTopic, fourthTopic}}}, []*types.Log{allLogs[5].Logs[0], allLogs[5].Logs[2]}, nil, nil},
		}
	)

	for i := range testCases {
		testCases[i].c = make(chan []*types.Log)
		testCases[i].sub, _ = api.events.SubscribeLogs(testCases[i].crit, testCases[i].c)
	}

	for n, test := range testCases {
		i := n
		tt := test
		go func() {
			var fetched []*types.Log
		fetchLoop:
			for {
				logs := <-tt.c
				fetched = append(fetched, logs...)
				if len(fetched) >= len(tt.expected) {
					break fetchLoop
				}
			}

			if len(fetched) != len(tt.expected) {
				panic(fmt.Sprintf("invalid number of logs for case %d, want %d log(s), got %d", i, len(tt.expected), len(fetched)))
			}

			for l := range fetched {
				if fetched[l].Removed {
					panic(fmt.Sprintf("expected log not to be removed for log %d in case %d", l, i))
				}
				if !reflect.DeepEqual(fetched[l], tt.expected[l]) {
					panic(fmt.Sprintf("invalid log on index %d for case %d", l, i))
				}
			}
		}()
	}

	time.Sleep(1 * time.Second)
	for _, l := range allLogs {
		if err := mux.Post(l); err != nil {
			t.Fatal(err)
		}
	}
}
