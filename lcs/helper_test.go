package lcs

import (
	"crypto/rand"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/5uwifi/canchain/can"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lcs/flowcontrol"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/light"
	"github.com/5uwifi/canchain/params"
)

var (
	testBankKey, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	testBankAddress = crypto.PubkeyToAddress(testBankKey.PublicKey)
	testBankFunds   = big.NewInt(1000000000000000000)

	acc1Key, _ = crypto.HexToECDSA("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
	acc2Key, _ = crypto.HexToECDSA("49a7b37aa6f6645917e7b807e9d1c00d4fa71f18343b0d4122a4d2df64dd6fee")
	acc1Addr   = crypto.PubkeyToAddress(acc1Key.PublicKey)
	acc2Addr   = crypto.PubkeyToAddress(acc2Key.PublicKey)

	testContractCode         = common.Hex2Bytes("606060405260cc8060106000396000f360606040526000357c01000000000000000000000000000000000000000000000000000000009004806360cd2685146041578063c16431b914606b57603f565b005b6055600480803590602001909190505060a9565b6040518082815260200191505060405180910390f35b60886004808035906020019091908035906020019091905050608a565b005b80600060005083606481101560025790900160005b50819055505b5050565b6000600060005082606481101560025790900160005b5054905060c7565b91905056")
	testContractAddr         common.Address
	testContractCodeDeployed = testContractCode[16:]
	testContractDeployed     = uint64(2)

	testEventEmitterCode = common.Hex2Bytes("60606040523415600e57600080fd5b7f57050ab73f6b9ebdd9f76b8d4997793f48cf956e965ee070551b9ca0bb71584e60405160405180910390a160358060476000396000f3006060604052600080fd00a165627a7a723058203f727efcad8b5811f8cb1fc2620ce5e8c63570d697aef968172de296ea3994140029")
	testEventEmitterAddr common.Address

	testBufLimit = uint64(100)
)

/*
contract test {

    uint256[100] data;

    function Put(uint256 addr, uint256 value) {
        data[addr] = value;
    }

    function Get(uint256 addr) constant returns (uint256 value) {
        return data[addr];
    }
}
*/

func testChainGen(i int, block *kernel.BlockGen) {
	signer := types.HomesteadSigner{}

	switch i {
	case 0:
		tx, _ := types.SignTx(types.NewTransaction(block.TxNonce(testBankAddress), acc1Addr, big.NewInt(10000), params.TxGas, nil, nil), signer, testBankKey)
		block.AddTx(tx)
	case 1:
		nonce := block.TxNonce(acc1Addr)

		tx1, _ := types.SignTx(types.NewTransaction(block.TxNonce(testBankAddress), acc1Addr, big.NewInt(1000), params.TxGas, nil, nil), signer, testBankKey)
		tx2, _ := types.SignTx(types.NewTransaction(nonce, acc2Addr, big.NewInt(1000), params.TxGas, nil, nil), signer, acc1Key)
		tx3, _ := types.SignTx(types.NewContractCreation(nonce+1, big.NewInt(0), 200000, big.NewInt(0), testContractCode), signer, acc1Key)
		testContractAddr = crypto.CreateAddress(acc1Addr, nonce+1)
		tx4, _ := types.SignTx(types.NewContractCreation(nonce+2, big.NewInt(0), 200000, big.NewInt(0), testEventEmitterCode), signer, acc1Key)
		testEventEmitterAddr = crypto.CreateAddress(acc1Addr, nonce+2)
		block.AddTx(tx1)
		block.AddTx(tx2)
		block.AddTx(tx3)
		block.AddTx(tx4)
	case 2:
		block.SetCoinbase(acc2Addr)
		block.SetExtra([]byte("yeehaw"))
		data := common.Hex2Bytes("C16431B900000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000000000000000000000001")
		tx, _ := types.SignTx(types.NewTransaction(block.TxNonce(testBankAddress), testContractAddr, big.NewInt(0), 100000, nil, data), signer, testBankKey)
		block.AddTx(tx)
	case 3:
		b2 := block.PrevBlock(1).Header()
		b2.Extra = []byte("foo")
		block.AddUncle(b2)
		b3 := block.PrevBlock(2).Header()
		b3.Extra = []byte("foo")
		block.AddUncle(b3)
		data := common.Hex2Bytes("C16431B900000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000002")
		tx, _ := types.SignTx(types.NewTransaction(block.TxNonce(testBankAddress), testContractAddr, big.NewInt(0), 100000, nil, data), signer, testBankKey)
		block.AddTx(tx)
	}
}

func testIndexers(db candb.Database, odr light.OdrBackend, iConfig *light.IndexerConfig) (*kernel.ChainIndexer, *kernel.ChainIndexer, *kernel.ChainIndexer) {
	chtIndexer := light.NewChtIndexer(db, odr, iConfig.ChtSize, iConfig.ChtConfirms)
	bloomIndexer := can.NewBloomIndexer(db, iConfig.BloomSize, iConfig.BloomConfirms)
	bloomTrieIndexer := light.NewBloomTrieIndexer(db, odr, iConfig.BloomSize, iConfig.BloomTrieSize)
	bloomIndexer.AddChildIndexer(bloomTrieIndexer)
	return chtIndexer, bloomIndexer, bloomTrieIndexer
}

func testRCL() RequestCostList {
	cl := make(RequestCostList, len(reqList))
	for i, code := range reqList {
		cl[i].MsgCode = code
		cl[i].BaseCost = 0
		cl[i].ReqCost = 0
	}
	return cl
}

func newTestProtocolManager(lightSync bool, blocks int, generator func(int, *kernel.BlockGen), odr *LesOdr, peers *peerSet, db candb.Database) (*ProtocolManager, error) {
	var (
		evmux  = new(event.TypeMux)
		engine = ethash.NewFaker()
		gspec  = kernel.Genesis{
			Config: params.TestChainConfig,
			Alloc:  kernel.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
		}
		genesis = gspec.MustCommit(db)
		chain   BlockChain
	)
	if peers == nil {
		peers = newPeerSet()
	}

	if lightSync {
		chain, _ = light.NewLightChain(odr, gspec.Config, engine)
	} else {
		blockchain, _ := kernel.NewBlockChain(db, nil, gspec.Config, engine, vm.Config{})
		gchain, _ := kernel.GenerateChain(gspec.Config, genesis, ethash.NewFaker(), db, blocks, generator)
		if _, err := blockchain.InsertChain(gchain); err != nil {
			panic(err)
		}
		chain = blockchain
	}

	indexConfig := light.TestServerIndexerConfig
	if lightSync {
		indexConfig = light.TestClientIndexerConfig
	}
	pm, err := NewProtocolManager(gspec.Config, indexConfig, lightSync, NetworkId, evmux, engine, peers, chain, nil, db, odr, nil, nil, make(chan struct{}), new(sync.WaitGroup))
	if err != nil {
		return nil, err
	}
	if !lightSync {
		srv := &LcsServer{lcsCommons: lcsCommons{protocolManager: pm}}
		pm.server = srv

		srv.defParams = &flowcontrol.ServerParams{
			BufLimit:    testBufLimit,
			MinRecharge: 1,
		}

		srv.fcManager = flowcontrol.NewClientManager(50, 10, 1000000000)
		srv.fcCostStats = newCostStats(nil)
	}
	pm.Start(1000)
	return pm, nil
}

func newTestProtocolManagerMust(t *testing.T, lightSync bool, blocks int, generator func(int, *kernel.BlockGen), odr *LesOdr, peers *peerSet, db candb.Database) *ProtocolManager {
	pm, err := newTestProtocolManager(lightSync, blocks, generator, odr, peers, db)
	if err != nil {
		t.Fatalf("Failed to create protocol manager: %v", err)
	}
	return pm
}

type testPeer struct {
	net p2p.MsgReadWriter
	app *p2p.MsgPipeRW
	*peer
}

func newTestPeer(t *testing.T, name string, version int, pm *ProtocolManager, shake bool) (*testPeer, <-chan error) {
	app, net := p2p.MsgPipe()

	var id discover.NodeID
	rand.Read(id[:])

	peer := pm.newPeer(version, NetworkId, p2p.NewPeer(id, name, nil), net)

	errc := make(chan error, 1)
	go func() {
		select {
		case pm.newPeerCh <- peer:
			errc <- pm.handle(peer)
		case <-pm.quitSync:
			errc <- p2p.DiscQuitting
		}
	}()
	tp := &testPeer{
		app:  app,
		net:  net,
		peer: peer,
	}
	if shake {
		var (
			genesis = pm.blockchain.Genesis()
			head    = pm.blockchain.CurrentHeader()
			td      = pm.blockchain.GetTd(head.Hash(), head.Number.Uint64())
		)
		tp.handshake(t, td, head.Hash(), head.Number.Uint64(), genesis.Hash())
	}
	return tp, errc
}

func newTestPeerPair(name string, version int, pm, pm2 *ProtocolManager) (*peer, <-chan error, *peer, <-chan error) {
	app, net := p2p.MsgPipe()

	var id discover.NodeID
	rand.Read(id[:])

	peer := pm.newPeer(version, NetworkId, p2p.NewPeer(id, name, nil), net)
	peer2 := pm2.newPeer(version, NetworkId, p2p.NewPeer(id, name, nil), app)

	errc := make(chan error, 1)
	errc2 := make(chan error, 1)
	go func() {
		select {
		case pm.newPeerCh <- peer:
			errc <- pm.handle(peer)
		case <-pm.quitSync:
			errc <- p2p.DiscQuitting
		}
	}()
	go func() {
		select {
		case pm2.newPeerCh <- peer2:
			errc2 <- pm2.handle(peer2)
		case <-pm2.quitSync:
			errc2 <- p2p.DiscQuitting
		}
	}()
	return peer, errc, peer2, errc2
}

func (p *testPeer) handshake(t *testing.T, td *big.Int, head common.Hash, headNum uint64, genesis common.Hash) {
	var expList keyValueList
	expList = expList.add("protocolVersion", uint64(p.version))
	expList = expList.add("networkId", uint64(NetworkId))
	expList = expList.add("headTd", td)
	expList = expList.add("headHash", head)
	expList = expList.add("headNum", headNum)
	expList = expList.add("genesisHash", genesis)
	sendList := make(keyValueList, len(expList))
	copy(sendList, expList)
	expList = expList.add("serveHeaders", nil)
	expList = expList.add("serveChainSince", uint64(0))
	expList = expList.add("serveStateSince", uint64(0))
	expList = expList.add("txRelay", nil)
	expList = expList.add("flowControl/BL", testBufLimit)
	expList = expList.add("flowControl/MRR", uint64(1))
	expList = expList.add("flowControl/MRC", testRCL())

	if err := p2p.ExpectMsg(p.app, StatusMsg, expList); err != nil {
		t.Fatalf("status recv: %v", err)
	}
	if err := p2p.Send(p.app, StatusMsg, sendList); err != nil {
		t.Fatalf("status send: %v", err)
	}

	p.fcServerParams = &flowcontrol.ServerParams{
		BufLimit:    testBufLimit,
		MinRecharge: 1,
	}
}

func (p *testPeer) close() {
	p.app.Close()
}

type TestEntity struct {
	db               candb.Database
	rPeer            *peer
	tPeer            *testPeer
	peers            *peerSet
	pm               *ProtocolManager
	chtIndexer       *kernel.ChainIndexer
	bloomIndexer     *kernel.ChainIndexer
	bloomTrieIndexer *kernel.ChainIndexer
}

func newServerEnv(t *testing.T, blocks int, protocol int, waitIndexers func(*kernel.ChainIndexer, *kernel.ChainIndexer, *kernel.ChainIndexer)) (*TestEntity, func()) {
	db := candb.NewMemDatabase()
	cIndexer, bIndexer, btIndexer := testIndexers(db, nil, light.TestServerIndexerConfig)

	pm := newTestProtocolManagerMust(t, false, blocks, testChainGen, nil, nil, db)
	peer, _ := newTestPeer(t, "peer", protocol, pm, true)

	cIndexer.Start(pm.blockchain.(*kernel.BlockChain))
	bIndexer.Start(pm.blockchain.(*kernel.BlockChain))

	if waitIndexers != nil {
		waitIndexers(cIndexer, bIndexer, btIndexer)
	}

	return &TestEntity{
			db:               db,
			tPeer:            peer,
			pm:               pm,
			chtIndexer:       cIndexer,
			bloomIndexer:     bIndexer,
			bloomTrieIndexer: btIndexer,
		}, func() {
			peer.close()
			cIndexer.Close()
			bIndexer.Close()
		}
}

func newClientServerEnv(t *testing.T, blocks int, protocol int, waitIndexers func(*kernel.ChainIndexer, *kernel.ChainIndexer, *kernel.ChainIndexer), newPeer bool) (*TestEntity, *TestEntity, func()) {
	db, ldb := candb.NewMemDatabase(), candb.NewMemDatabase()
	peers, lPeers := newPeerSet(), newPeerSet()

	dist := newRequestDistributor(lPeers, make(chan struct{}))
	rm := newRetrieveManager(lPeers, dist, nil)
	odr := NewLesOdr(ldb, light.TestClientIndexerConfig, rm)

	cIndexer, bIndexer, btIndexer := testIndexers(db, nil, light.TestServerIndexerConfig)
	lcIndexer, lbIndexer, lbtIndexer := testIndexers(ldb, odr, light.TestClientIndexerConfig)
	odr.SetIndexers(lcIndexer, lbtIndexer, lbIndexer)

	pm := newTestProtocolManagerMust(t, false, blocks, testChainGen, nil, peers, db)
	lpm := newTestProtocolManagerMust(t, true, 0, nil, odr, lPeers, ldb)

	startIndexers := func(clientMode bool, pm *ProtocolManager) {
		if clientMode {
			lcIndexer.Start(pm.blockchain.(*light.LightChain))
			lbIndexer.Start(pm.blockchain.(*light.LightChain))
		} else {
			cIndexer.Start(pm.blockchain.(*kernel.BlockChain))
			bIndexer.Start(pm.blockchain.(*kernel.BlockChain))
		}
	}

	startIndexers(false, pm)
	startIndexers(true, lpm)

	if waitIndexers != nil {
		waitIndexers(cIndexer, bIndexer, btIndexer)
	}

	var (
		peer, lPeer *peer
		err1, err2  <-chan error
	)
	if newPeer {
		peer, err1, lPeer, err2 = newTestPeerPair("peer", protocol, pm, lpm)
		select {
		case <-time.After(time.Millisecond * 100):
		case err := <-err1:
			t.Fatalf("peer 1 handshake error: %v", err)
		case err := <-err2:
			t.Fatalf("peer 2 handshake error: %v", err)
		}
	}

	return &TestEntity{
			db:               db,
			pm:               pm,
			rPeer:            peer,
			peers:            peers,
			chtIndexer:       cIndexer,
			bloomIndexer:     bIndexer,
			bloomTrieIndexer: btIndexer,
		}, &TestEntity{
			db:               ldb,
			pm:               lpm,
			rPeer:            lPeer,
			peers:            lPeers,
			chtIndexer:       lcIndexer,
			bloomIndexer:     lbIndexer,
			bloomTrieIndexer: lbtIndexer,
		}, func() {
			cIndexer.Close()
			bIndexer.Close()
			lcIndexer.Close()
			lbIndexer.Close()
		}
}
