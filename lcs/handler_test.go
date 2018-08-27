package lcs

import (
	"encoding/binary"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/lib/trie"
	"github.com/5uwifi/canchain/light"
	"github.com/5uwifi/canchain/params"
)

func expectResponse(r p2p.MsgReader, msgcode, reqID, bv uint64, data interface{}) error {
	type resp struct {
		ReqID, BV uint64
		Data      interface{}
	}
	return p2p.ExpectMsg(r, msgcode, resp{reqID, bv, data})
}

func TestGetBlockHeadersLes1(t *testing.T) { testGetBlockHeaders(t, 1) }
func TestGetBlockHeadersLes2(t *testing.T) { testGetBlockHeaders(t, 2) }

func testGetBlockHeaders(t *testing.T, protocol int) {
	pm := newTestProtocolManagerMust(t, false, downloader.MaxHashFetch+15, nil, nil, nil, candb.NewMemDatabase())
	bc := pm.blockchain.(*kernel.BlockChain)
	peer, _ := newTestPeer(t, "peer", protocol, pm, true)
	defer peer.close()

	var unknown common.Hash
	for i := range unknown {
		unknown[i] = byte(i)
	}
	limit := uint64(MaxHeaderFetch)
	tests := []struct {
		query  *getBlockHeadersData
		expect []common.Hash
	}{
		{
			&getBlockHeadersData{Origin: hashOrNumber{Hash: bc.GetBlockByNumber(limit / 2).Hash()}, Amount: 1},
			[]common.Hash{bc.GetBlockByNumber(limit / 2).Hash()},
		}, {
			&getBlockHeadersData{Origin: hashOrNumber{Number: limit / 2}, Amount: 1},
			[]common.Hash{bc.GetBlockByNumber(limit / 2).Hash()},
		},
		{
			&getBlockHeadersData{Origin: hashOrNumber{Number: limit / 2}, Amount: 3},
			[]common.Hash{
				bc.GetBlockByNumber(limit / 2).Hash(),
				bc.GetBlockByNumber(limit/2 + 1).Hash(),
				bc.GetBlockByNumber(limit/2 + 2).Hash(),
			},
		}, {
			&getBlockHeadersData{Origin: hashOrNumber{Number: limit / 2}, Amount: 3, Reverse: true},
			[]common.Hash{
				bc.GetBlockByNumber(limit / 2).Hash(),
				bc.GetBlockByNumber(limit/2 - 1).Hash(),
				bc.GetBlockByNumber(limit/2 - 2).Hash(),
			},
		},
		{
			&getBlockHeadersData{Origin: hashOrNumber{Number: limit / 2}, Skip: 3, Amount: 3},
			[]common.Hash{
				bc.GetBlockByNumber(limit / 2).Hash(),
				bc.GetBlockByNumber(limit/2 + 4).Hash(),
				bc.GetBlockByNumber(limit/2 + 8).Hash(),
			},
		}, {
			&getBlockHeadersData{Origin: hashOrNumber{Number: limit / 2}, Skip: 3, Amount: 3, Reverse: true},
			[]common.Hash{
				bc.GetBlockByNumber(limit / 2).Hash(),
				bc.GetBlockByNumber(limit/2 - 4).Hash(),
				bc.GetBlockByNumber(limit/2 - 8).Hash(),
			},
		},
		{
			&getBlockHeadersData{Origin: hashOrNumber{Number: 0}, Amount: 1},
			[]common.Hash{bc.GetBlockByNumber(0).Hash()},
		}, {
			&getBlockHeadersData{Origin: hashOrNumber{Number: bc.CurrentBlock().NumberU64()}, Amount: 1},
			[]common.Hash{bc.CurrentBlock().Hash()},
		},
		/*{
			&getBlockHeadersData{Origin: hashOrNumber{Number: bc.CurrentBlock().NumberU64() - 1}, Amount: limit + 10, Reverse: true},
			bc.GetBlockHashesFromHash(bc.CurrentBlock().Hash(), limit),
		},*/
		{
			&getBlockHeadersData{Origin: hashOrNumber{Number: bc.CurrentBlock().NumberU64() - 4}, Skip: 3, Amount: 3},
			[]common.Hash{
				bc.GetBlockByNumber(bc.CurrentBlock().NumberU64() - 4).Hash(),
				bc.GetBlockByNumber(bc.CurrentBlock().NumberU64()).Hash(),
			},
		}, {
			&getBlockHeadersData{Origin: hashOrNumber{Number: 4}, Skip: 3, Amount: 3, Reverse: true},
			[]common.Hash{
				bc.GetBlockByNumber(4).Hash(),
				bc.GetBlockByNumber(0).Hash(),
			},
		},
		{
			&getBlockHeadersData{Origin: hashOrNumber{Number: bc.CurrentBlock().NumberU64() - 4}, Skip: 2, Amount: 3},
			[]common.Hash{
				bc.GetBlockByNumber(bc.CurrentBlock().NumberU64() - 4).Hash(),
				bc.GetBlockByNumber(bc.CurrentBlock().NumberU64() - 1).Hash(),
			},
		}, {
			&getBlockHeadersData{Origin: hashOrNumber{Number: 4}, Skip: 2, Amount: 3, Reverse: true},
			[]common.Hash{
				bc.GetBlockByNumber(4).Hash(),
				bc.GetBlockByNumber(1).Hash(),
			},
		},
		{
			&getBlockHeadersData{Origin: hashOrNumber{Hash: unknown}, Amount: 1},
			[]common.Hash{},
		}, {
			&getBlockHeadersData{Origin: hashOrNumber{Number: bc.CurrentBlock().NumberU64() + 1}, Amount: 1},
			[]common.Hash{},
		},
	}
	var reqID uint64
	for i, tt := range tests {
		headers := []*types.Header{}
		for _, hash := range tt.expect {
			headers = append(headers, bc.GetHeaderByHash(hash))
		}
		reqID++
		cost := peer.GetRequestCost(GetBlockHeadersMsg, int(tt.query.Amount))
		sendRequest(peer.app, GetBlockHeadersMsg, reqID, cost, tt.query)
		if err := expectResponse(peer.app, BlockHeadersMsg, reqID, testBufLimit, headers); err != nil {
			t.Errorf("test %d: headers mismatch: %v", i, err)
		}
	}
}

func TestGetBlockBodiesLes1(t *testing.T) { testGetBlockBodies(t, 1) }
func TestGetBlockBodiesLes2(t *testing.T) { testGetBlockBodies(t, 2) }

func testGetBlockBodies(t *testing.T, protocol int) {
	pm := newTestProtocolManagerMust(t, false, downloader.MaxBlockFetch+15, nil, nil, nil, candb.NewMemDatabase())
	bc := pm.blockchain.(*kernel.BlockChain)
	peer, _ := newTestPeer(t, "peer", protocol, pm, true)
	defer peer.close()

	limit := MaxBodyFetch
	tests := []struct {
		random    int
		explicit  []common.Hash
		available []bool
		expected  int
	}{
		{1, nil, nil, 1},
		{10, nil, nil, 10},
		{limit, nil, nil, limit},
		{0, []common.Hash{bc.Genesis().Hash()}, []bool{true}, 1},
		{0, []common.Hash{bc.CurrentBlock().Hash()}, []bool{true}, 1},
		{0, []common.Hash{{}}, []bool{false}, 0},

		{0, []common.Hash{
			{},
			bc.GetBlockByNumber(1).Hash(),
			{},
			bc.GetBlockByNumber(10).Hash(),
			{},
			bc.GetBlockByNumber(100).Hash(),
			{},
		}, []bool{false, true, false, true, false, true, false}, 3},
	}
	var reqID uint64
	for i, tt := range tests {
		hashes, seen := []common.Hash{}, make(map[int64]bool)
		bodies := []*types.Body{}

		for j := 0; j < tt.random; j++ {
			for {
				num := rand.Int63n(int64(bc.CurrentBlock().NumberU64()))
				if !seen[num] {
					seen[num] = true

					block := bc.GetBlockByNumber(uint64(num))
					hashes = append(hashes, block.Hash())
					if len(bodies) < tt.expected {
						bodies = append(bodies, &types.Body{Transactions: block.Transactions(), Uncles: block.Uncles()})
					}
					break
				}
			}
		}
		for j, hash := range tt.explicit {
			hashes = append(hashes, hash)
			if tt.available[j] && len(bodies) < tt.expected {
				block := bc.GetBlockByHash(hash)
				bodies = append(bodies, &types.Body{Transactions: block.Transactions(), Uncles: block.Uncles()})
			}
		}
		reqID++
		cost := peer.GetRequestCost(GetBlockBodiesMsg, len(hashes))
		sendRequest(peer.app, GetBlockBodiesMsg, reqID, cost, hashes)
		if err := expectResponse(peer.app, BlockBodiesMsg, reqID, testBufLimit, bodies); err != nil {
			t.Errorf("test %d: bodies mismatch: %v", i, err)
		}
	}
}

func TestGetCodeLes1(t *testing.T) { testGetCode(t, 1) }
func TestGetCodeLes2(t *testing.T) { testGetCode(t, 2) }

func testGetCode(t *testing.T, protocol int) {
	pm := newTestProtocolManagerMust(t, false, 4, testChainGen, nil, nil, candb.NewMemDatabase())
	bc := pm.blockchain.(*kernel.BlockChain)
	peer, _ := newTestPeer(t, "peer", protocol, pm, true)
	defer peer.close()

	var codereqs []*CodeReq
	var codes [][]byte

	for i := uint64(0); i <= bc.CurrentBlock().NumberU64(); i++ {
		header := bc.GetHeaderByNumber(i)
		req := &CodeReq{
			BHash:  header.Hash(),
			AccKey: crypto.Keccak256(testContractAddr[:]),
		}
		codereqs = append(codereqs, req)
		if i >= testContractDeployed {
			codes = append(codes, testContractCodeDeployed)
		}
	}

	cost := peer.GetRequestCost(GetCodeMsg, len(codereqs))
	sendRequest(peer.app, GetCodeMsg, 42, cost, codereqs)
	if err := expectResponse(peer.app, CodeMsg, 42, testBufLimit, codes); err != nil {
		t.Errorf("codes mismatch: %v", err)
	}
}

func TestGetReceiptLes1(t *testing.T) { testGetReceipt(t, 1) }
func TestGetReceiptLes2(t *testing.T) { testGetReceipt(t, 2) }

func testGetReceipt(t *testing.T, protocol int) {
	db := candb.NewMemDatabase()
	pm := newTestProtocolManagerMust(t, false, 4, testChainGen, nil, nil, db)
	bc := pm.blockchain.(*kernel.BlockChain)
	peer, _ := newTestPeer(t, "peer", protocol, pm, true)
	defer peer.close()

	hashes, receipts := []common.Hash{}, []types.Receipts{}
	for i := uint64(0); i <= bc.CurrentBlock().NumberU64(); i++ {
		block := bc.GetBlockByNumber(i)

		hashes = append(hashes, block.Hash())
		receipts = append(receipts, rawdb.ReadReceipts(db, block.Hash(), block.NumberU64()))
	}
	cost := peer.GetRequestCost(GetReceiptsMsg, len(hashes))
	sendRequest(peer.app, GetReceiptsMsg, 42, cost, hashes)
	if err := expectResponse(peer.app, ReceiptsMsg, 42, testBufLimit, receipts); err != nil {
		t.Errorf("receipts mismatch: %v", err)
	}
}

func TestGetProofsLes1(t *testing.T) { testGetProofs(t, 1) }
func TestGetProofsLes2(t *testing.T) { testGetProofs(t, 2) }

func testGetProofs(t *testing.T, protocol int) {
	db := candb.NewMemDatabase()
	pm := newTestProtocolManagerMust(t, false, 4, testChainGen, nil, nil, db)
	bc := pm.blockchain.(*kernel.BlockChain)
	peer, _ := newTestPeer(t, "peer", protocol, pm, true)
	defer peer.close()

	var (
		proofreqs []ProofReq
		proofsV1  [][]rlp.RawValue
	)
	proofsV2 := light.NewNodeSet()

	accounts := []common.Address{testBankAddress, acc1Addr, acc2Addr, {}}
	for i := uint64(0); i <= bc.CurrentBlock().NumberU64(); i++ {
		header := bc.GetHeaderByNumber(i)
		root := header.Root
		trie, _ := trie.New(root, trie.NewDatabase(db))

		for _, acc := range accounts {
			req := ProofReq{
				BHash: header.Hash(),
				Key:   crypto.Keccak256(acc[:]),
			}
			proofreqs = append(proofreqs, req)

			switch protocol {
			case 1:
				var proof light.NodeList
				trie.Prove(crypto.Keccak256(acc[:]), 0, &proof)
				proofsV1 = append(proofsV1, proof)
			case 2:
				trie.Prove(crypto.Keccak256(acc[:]), 0, proofsV2)
			}
		}
	}
	switch protocol {
	case 1:
		cost := peer.GetRequestCost(GetProofsV1Msg, len(proofreqs))
		sendRequest(peer.app, GetProofsV1Msg, 42, cost, proofreqs)
		if err := expectResponse(peer.app, ProofsV1Msg, 42, testBufLimit, proofsV1); err != nil {
			t.Errorf("proofs mismatch: %v", err)
		}
	case 2:
		cost := peer.GetRequestCost(GetProofsV2Msg, len(proofreqs))
		sendRequest(peer.app, GetProofsV2Msg, 42, cost, proofreqs)
		if err := expectResponse(peer.app, ProofsV2Msg, 42, testBufLimit, proofsV2.NodeList()); err != nil {
			t.Errorf("proofs mismatch: %v", err)
		}
	}
}

func TestGetCHTProofsLes1(t *testing.T) { testGetCHTProofs(t, 1) }
func TestGetCHTProofsLes2(t *testing.T) { testGetCHTProofs(t, 2) }

func testGetCHTProofs(t *testing.T, protocol int) {
	frequency := uint64(light.CHTFrequencyClient)
	if protocol == 1 {
		frequency = uint64(light.CHTFrequencyServer)
	}
	db := candb.NewMemDatabase()
	pm := newTestProtocolManagerMust(t, false, int(frequency)+light.HelperTrieProcessConfirmations, testChainGen, nil, nil, db)
	bc := pm.blockchain.(*kernel.BlockChain)
	peer, _ := newTestPeer(t, "peer", protocol, pm, true)
	defer peer.close()

	time.Sleep(100 * time.Millisecond * time.Duration(frequency/light.CHTFrequencyServer))
	time.Sleep(250 * time.Millisecond)

	header := bc.GetHeaderByNumber(frequency)
	rlp, _ := rlp.EncodeToBytes(header)

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, frequency)

	proofsV1 := []ChtResp{{
		Header: header,
	}}
	proofsV2 := HelperTrieResps{
		AuxData: [][]byte{rlp},
	}
	switch protocol {
	case 1:
		root := light.GetChtRoot(db, 0, bc.GetHeaderByNumber(frequency-1).Hash())
		trie, _ := trie.New(root, trie.NewDatabase(candb.NewTable(db, light.ChtTablePrefix)))

		var proof light.NodeList
		trie.Prove(key, 0, &proof)
		proofsV1[0].Proof = proof

	case 2:
		root := light.GetChtV2Root(db, 0, bc.GetHeaderByNumber(frequency-1).Hash())
		trie, _ := trie.New(root, trie.NewDatabase(candb.NewTable(db, light.ChtTablePrefix)))
		trie.Prove(key, 0, &proofsV2.Proofs)
	}
	requestsV1 := []ChtReq{{
		ChtNum:   1,
		BlockNum: frequency,
	}}
	requestsV2 := []HelperTrieReq{{
		Type:    htCanonical,
		TrieIdx: 0,
		Key:     key,
		AuxReq:  auxHeader,
	}}
	switch protocol {
	case 1:
		cost := peer.GetRequestCost(GetHeaderProofsMsg, len(requestsV1))
		sendRequest(peer.app, GetHeaderProofsMsg, 42, cost, requestsV1)
		if err := expectResponse(peer.app, HeaderProofsMsg, 42, testBufLimit, proofsV1); err != nil {
			t.Errorf("proofs mismatch: %v", err)
		}
	case 2:
		cost := peer.GetRequestCost(GetHelperTrieProofsMsg, len(requestsV2))
		sendRequest(peer.app, GetHelperTrieProofsMsg, 42, cost, requestsV2)
		if err := expectResponse(peer.app, HelperTrieProofsMsg, 42, testBufLimit, proofsV2); err != nil {
			t.Errorf("proofs mismatch: %v", err)
		}
	}
}

func TestGetBloombitsProofs(t *testing.T) {
	db := candb.NewMemDatabase()
	pm := newTestProtocolManagerMust(t, false, light.BloomTrieFrequency+256, testChainGen, nil, nil, db)
	bc := pm.blockchain.(*kernel.BlockChain)
	peer, _ := newTestPeer(t, "peer", 2, pm, true)
	defer peer.close()

	time.Sleep(100 * time.Millisecond * time.Duration(light.BloomTrieFrequency/4096))
	time.Sleep(250 * time.Millisecond)

	for bit := 0; bit < 2048; bit++ {
		key := make([]byte, 10)

		binary.BigEndian.PutUint16(key[:2], uint16(bit))
		binary.BigEndian.PutUint64(key[2:], uint64(light.BloomTrieFrequency))

		requests := []HelperTrieReq{{
			Type:    htBloomBits,
			TrieIdx: 0,
			Key:     key,
		}}
		var proofs HelperTrieResps

		root := light.GetBloomTrieRoot(db, 0, bc.GetHeaderByNumber(light.BloomTrieFrequency-1).Hash())
		trie, _ := trie.New(root, trie.NewDatabase(candb.NewTable(db, light.BloomTrieTablePrefix)))
		trie.Prove(key, 0, &proofs.Proofs)

		cost := peer.GetRequestCost(GetHelperTrieProofsMsg, len(requests))
		sendRequest(peer.app, GetHelperTrieProofsMsg, 42, cost, requests)
		if err := expectResponse(peer.app, HelperTrieProofsMsg, 42, testBufLimit, proofs); err != nil {
			t.Errorf("bit %d: proofs mismatch: %v", bit, err)
		}
	}
}

func TestTransactionStatusLes2(t *testing.T) {
	db := candb.NewMemDatabase()
	pm := newTestProtocolManagerMust(t, false, 0, nil, nil, nil, db)
	chain := pm.blockchain.(*kernel.BlockChain)
	config := kernel.DefaultTxPoolConfig
	config.Journal = ""
	txpool := kernel.NewTxPool(config, params.TestChainConfig, chain)
	pm.txpool = txpool
	peer, _ := newTestPeer(t, "peer", 2, pm, true)
	defer peer.close()

	var reqID uint64

	test := func(tx *types.Transaction, send bool, expStatus txStatus) {
		reqID++
		if send {
			cost := peer.GetRequestCost(SendTxV2Msg, 1)
			sendRequest(peer.app, SendTxV2Msg, reqID, cost, types.Transactions{tx})
		} else {
			cost := peer.GetRequestCost(GetTxStatusMsg, 1)
			sendRequest(peer.app, GetTxStatusMsg, reqID, cost, []common.Hash{tx.Hash()})
		}
		if err := expectResponse(peer.app, TxStatusMsg, reqID, testBufLimit, []txStatus{expStatus}); err != nil {
			t.Errorf("transaction status mismatch")
		}
	}

	signer := types.HomesteadSigner{}

	tx0, _ := types.SignTx(types.NewTransaction(0, acc1Addr, big.NewInt(10000), params.TxGas, nil, nil), signer, testBankKey)
	test(tx0, true, txStatus{Status: kernel.TxStatusUnknown, Error: kernel.ErrUnderpriced.Error()})

	tx1, _ := types.SignTx(types.NewTransaction(0, acc1Addr, big.NewInt(10000), params.TxGas, big.NewInt(100000000000), nil), signer, testBankKey)
	test(tx1, false, txStatus{Status: kernel.TxStatusUnknown})
	test(tx1, true, txStatus{Status: kernel.TxStatusPending})
	test(tx1, true, txStatus{Status: kernel.TxStatusPending})

	tx2, _ := types.SignTx(types.NewTransaction(1, acc1Addr, big.NewInt(10000), params.TxGas, big.NewInt(100000000000), nil), signer, testBankKey)
	tx3, _ := types.SignTx(types.NewTransaction(2, acc1Addr, big.NewInt(10000), params.TxGas, big.NewInt(100000000000), nil), signer, testBankKey)
	test(tx3, true, txStatus{Status: kernel.TxStatusQueued})
	test(tx2, true, txStatus{Status: kernel.TxStatusPending})
	test(tx3, false, txStatus{Status: kernel.TxStatusPending})

	gchain, _ := kernel.GenerateChain(params.TestChainConfig, chain.GetBlockByNumber(0), ethash.NewFaker(), db, 1, func(i int, block *kernel.BlockGen) {
		block.AddTx(tx1)
		block.AddTx(tx2)
	})
	if _, err := chain.InsertChain(gchain); err != nil {
		panic(err)
	}
	for i := 0; i < 10; i++ {
		if pending, _ := txpool.Stats(); pending == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pending, _ := txpool.Stats(); pending != 1 {
		t.Fatalf("pending count mismatch: have %d, want 1", pending)
	}

	block1hash := rawdb.ReadCanonicalHash(db, 1)
	test(tx1, false, txStatus{Status: kernel.TxStatusIncluded, Lookup: &rawdb.TxLookupEntry{BlockHash: block1hash, BlockIndex: 1, Index: 0}})
	test(tx2, false, txStatus{Status: kernel.TxStatusIncluded, Lookup: &rawdb.TxLookupEntry{BlockHash: block1hash, BlockIndex: 1, Index: 1}})

	gchain, _ = kernel.GenerateChain(params.TestChainConfig, chain.GetBlockByNumber(0), ethash.NewFaker(), db, 2, func(i int, block *kernel.BlockGen) {})
	if _, err := chain.InsertChain(gchain); err != nil {
		panic(err)
	}
	for i := 0; i < 10; i++ {
		if pending, _ := txpool.Stats(); pending == 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pending, _ := txpool.Stats(); pending != 3 {
		t.Fatalf("pending count mismatch: have %d, want 3", pending)
	}
	test(tx1, false, txStatus{Status: kernel.TxStatusPending})
	test(tx2, false, txStatus{Status: kernel.TxStatusPending})
}
