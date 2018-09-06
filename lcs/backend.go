package lcs

import (
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/can"
	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/can/filters"
	"github.com/5uwifi/canchain/can/gasprice"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/bloombits"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/discv5"
	"github.com/5uwifi/canchain/light"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/privacy/canapi"
	"github.com/5uwifi/canchain/rpc"
)

type LightCANChain struct {
	lcsCommons

	odr          *LesOdr
	relay        *LesTxRelay
	chainConfig  *params.ChainConfig
	shutdownChan chan bool

	peers      *peerSet
	txPool     *light.TxPool
	blockchain *light.LightChain
	serverPool *serverPool
	reqDist    *requestDistributor
	retriever  *retrieveManager

	bloomRequests chan chan *bloombits.Retrieval
	bloomIndexer  *kernel.ChainIndexer

	ApiBackend *LcsApiBackend

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	networkId     uint64
	netRPCService *canapi.PublicNetAPI

	wg sync.WaitGroup
}

func New(ctx *node.ServiceContext, config *can.Config) (*LightCANChain, error) {
	chainDb, err := can.CreateDB(ctx, config, "lightchaindata")
	if err != nil {
		return nil, err
	}
	chainConfig, genesisHash, genesisErr := kernel.SetupGenesisBlock(chainDb, config.Genesis)
	if _, isCompat := genesisErr.(*params.ConfigCompatError); genesisErr != nil && !isCompat {
		return nil, genesisErr
	}
	log4j.Info("Initialised chain configuration", "config", chainConfig)

	peers := newPeerSet()
	quitSync := make(chan struct{})

	leth := &LightCANChain{
		lcsCommons: lcsCommons{
			chainDb: chainDb,
			config:  config,
			iConfig: light.DefaultClientIndexerConfig,
		},
		chainConfig:    chainConfig,
		eventMux:       ctx.EventMux,
		peers:          peers,
		reqDist:        newRequestDistributor(peers, quitSync),
		accountManager: ctx.AccountManager,
		engine:         can.CreateConsensusEngine(ctx, chainConfig, &config.Ethash, nil, false, chainDb),
		shutdownChan:   make(chan bool),
		networkId:      config.NetworkId,
		bloomRequests:  make(chan chan *bloombits.Retrieval),
		bloomIndexer:   can.NewBloomIndexer(chainDb, params.BloomBitsBlocksClient, params.HelperTrieConfirmations),
	}

	leth.relay = NewLesTxRelay(peers, leth.reqDist)
	leth.serverPool = newServerPool(chainDb, quitSync, &leth.wg)
	leth.retriever = newRetrieveManager(peers, leth.reqDist, leth.serverPool)

	leth.odr = NewLesOdr(chainDb, light.DefaultClientIndexerConfig, leth.retriever)
	leth.chtIndexer = light.NewChtIndexer(chainDb, leth.odr, params.CHTFrequencyClient, params.HelperTrieConfirmations)
	leth.bloomTrieIndexer = light.NewBloomTrieIndexer(chainDb, leth.odr, params.BloomBitsBlocksClient, params.BloomTrieFrequency)
	leth.odr.SetIndexers(leth.chtIndexer, leth.bloomTrieIndexer, leth.bloomIndexer)

	if leth.blockchain, err = light.NewLightChain(leth.odr, leth.chainConfig, leth.engine); err != nil {
		return nil, err
	}
	leth.bloomIndexer.AddChildIndexer(leth.bloomTrieIndexer)
	leth.chtIndexer.Start(leth.blockchain)
	leth.bloomIndexer.Start(leth.blockchain)

	if compat, ok := genesisErr.(*params.ConfigCompatError); ok {
		log4j.Warn("Rewinding chain to upgrade configuration", "err", compat)
		leth.blockchain.SetHead(compat.RewindTo)
		rawdb.WriteChainConfig(chainDb, genesisHash, chainConfig)
	}

	leth.txPool = light.NewTxPool(leth.chainConfig, leth.blockchain, leth.relay)
	if leth.protocolManager, err = NewProtocolManager(leth.chainConfig, light.DefaultClientIndexerConfig, true, config.NetworkId, leth.eventMux, leth.engine, leth.peers, leth.blockchain, nil, chainDb, leth.odr, leth.relay, leth.serverPool, quitSync, &leth.wg); err != nil {
		return nil, err
	}
	leth.ApiBackend = &LcsApiBackend{leth, nil}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.MinerGasPrice
	}
	leth.ApiBackend.gpo = gasprice.NewOracle(leth.ApiBackend, gpoParams)
	return leth, nil
}

func lcsTopic(genesisHash common.Hash, protocolVersion uint) discv5.Topic {
	var name string
	switch protocolVersion {
	case lpv1:
		name = "LES"
	case lpv2:
		name = "LES2"
	default:
		panic(nil)
	}
	return discv5.Topic(name + "@" + common.Bytes2Hex(genesisHash.Bytes()[0:8]))
}

type LightDummyAPI struct{}

func (s *LightDummyAPI) Canerbase() (common.Address, error) {
	return common.Address{}, fmt.Errorf("not supported")
}

func (s *LightDummyAPI) Coinbase() (common.Address, error) {
	return common.Address{}, fmt.Errorf("not supported")
}

func (s *LightDummyAPI) Hashrate() hexutil.Uint {
	return 0
}

func (s *LightDummyAPI) Mining() bool {
	return false
}

func (s *LightCANChain) APIs() []rpc.API {
	return append(canapi.GetAPIs(s.ApiBackend), []rpc.API{
		{
			Namespace: "can",
			Version:   "1.0",
			Service:   &LightDummyAPI{},
			Public:    true,
		}, {
			Namespace: "can",
			Version:   "1.0",
			Service:   downloader.NewPublicDownloaderAPI(s.protocolManager.downloader, s.eventMux),
			Public:    true,
		}, {
			Namespace: "can",
			Version:   "1.0",
			Service:   filters.NewPublicFilterAPI(s.ApiBackend, true),
			Public:    true,
		}, {
			Namespace: "net",
			Version:   "1.0",
			Service:   s.netRPCService,
			Public:    true,
		},
	}...)
}

func (s *LightCANChain) ResetWithGenesisBlock(gb *types.Block) {
	s.blockchain.ResetWithGenesisBlock(gb)
}

func (s *LightCANChain) BlockChain() *light.LightChain      { return s.blockchain }
func (s *LightCANChain) TxPool() *light.TxPool              { return s.txPool }
func (s *LightCANChain) Engine() consensus.Engine           { return s.engine }
func (s *LightCANChain) LcsVersion() int                    { return int(ClientProtocolVersions[0]) }
func (s *LightCANChain) Downloader() *downloader.Downloader { return s.protocolManager.downloader }
func (s *LightCANChain) EventMux() *event.TypeMux           { return s.eventMux }

func (s *LightCANChain) Protocols() []p2p.Protocol {
	return s.makeProtocols(ClientProtocolVersions)
}

func (s *LightCANChain) Start(srvr *p2p.Server) error {
	log4j.Warn("Light client mode is an experimental feature")
	s.startBloomHandlers(params.BloomBitsBlocksClient)
	s.netRPCService = canapi.NewPublicNetAPI(srvr, s.networkId)
	protocolVersion := AdvertiseProtocolVersions[0]
	s.serverPool.start(srvr, lcsTopic(s.blockchain.Genesis().Hash(), protocolVersion))
	s.protocolManager.Start(s.config.LightPeers)
	return nil
}

func (s *LightCANChain) Stop() error {
	s.odr.Stop()
	s.bloomIndexer.Close()
	s.chtIndexer.Close()
	s.blockchain.Stop()
	s.protocolManager.Stop()
	s.txPool.Stop()
	s.engine.Close()

	s.eventMux.Stop()

	time.Sleep(time.Millisecond * 200)
	s.chainDb.Close()
	close(s.shutdownChan)

	return nil
}
