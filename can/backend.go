package can

import (
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/can/filters"
	"github.com/5uwifi/canchain/can/gasprice"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/bloombits"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/consensus/clique"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/miner"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/privacy/canapi"
	"github.com/5uwifi/canchain/rpc"
)

type LesServer interface {
	Start(srvr *p2p.Server)
	Stop()
	Protocols() []p2p.Protocol
	SetBloomBitsIndexer(bbIndexer *kernel.ChainIndexer)
}

type CANChain struct {
	config      *Config
	chainConfig *params.ChainConfig

	shutdownChan chan bool

	txPool          *kernel.TxPool
	blockchain      *kernel.BlockChain
	protocolManager *ProtocolManager
	lesServer       LesServer

	chainDb candb.Database

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	bloomRequests chan chan *bloombits.Retrieval
	bloomIndexer  *kernel.ChainIndexer

	APIBackend *EthAPIBackend

	miner     *miner.Miner
	gasPrice  *big.Int
	canerbase common.Address

	networkID     uint64
	netRPCService *canapi.PublicNetAPI

	lock sync.RWMutex
}

func (s *CANChain) AddLesServer(ls LesServer) {
	s.lesServer = ls
	ls.SetBloomBitsIndexer(s.bloomIndexer)
}

func New(ctx *node.ServiceContext, config *Config) (*CANChain, error) {
	if config.SyncMode == downloader.LightSync {
		return nil, errors.New("can't run eth.CANChain in light sync mode, use les.LightEthereum")
	}
	if !config.SyncMode.IsValid() {
		return nil, fmt.Errorf("invalid sync mode %d", config.SyncMode)
	}
	if config.MinerGasPrice == nil || config.MinerGasPrice.Cmp(common.Big0) <= 0 {
		log4j.Warn("Sanitizing invalid miner gas price", "provided", config.MinerGasPrice, "updated", DefaultConfig.MinerGasPrice)
		config.MinerGasPrice = new(big.Int).Set(DefaultConfig.MinerGasPrice)
	}
	chainDb, err := CreateDB(ctx, config, "chaindata")
	if err != nil {
		return nil, err
	}
	chainConfig, genesisHash, genesisErr := kernel.SetupGenesisBlock(chainDb, config.Genesis)
	if _, ok := genesisErr.(*params.ConfigCompatError); genesisErr != nil && !ok {
		return nil, genesisErr
	}
	log4j.Info("Initialised chain configuration", "config", chainConfig)

	eth := &CANChain{
		config:         config,
		chainDb:        chainDb,
		chainConfig:    chainConfig,
		eventMux:       ctx.EventMux,
		accountManager: ctx.AccountManager,
		engine:         CreateConsensusEngine(ctx, chainConfig, &config.Ethash, config.MinerNotify, config.MinerNoverify, chainDb),
		shutdownChan:   make(chan bool),
		networkID:      config.NetworkId,
		gasPrice:       config.MinerGasPrice,
		canerbase:      config.Canerbase,
		bloomRequests:  make(chan chan *bloombits.Retrieval),
		bloomIndexer:   NewBloomIndexer(chainDb, params.BloomBitsBlocks, params.BloomConfirms),
	}

	log4j.Info("Initialising CANChain protocol", "versions", ProtocolVersions, "network", config.NetworkId)

	if !config.SkipBcVersionCheck {
		bcVersion := rawdb.ReadDatabaseVersion(chainDb)
		if bcVersion != kernel.BlockChainVersion && bcVersion != 0 {
			return nil, fmt.Errorf("Blockchain DB version mismatch (%d / %d).\n", bcVersion, kernel.BlockChainVersion)
		}
		rawdb.WriteDatabaseVersion(chainDb, kernel.BlockChainVersion)
	}
	var (
		vmConfig    = vm.Config{EnablePreimageRecording: config.EnablePreimageRecording}
		cacheConfig = &kernel.CacheConfig{Disabled: config.NoPruning, TrieNodeLimit: config.TrieCache, TrieTimeLimit: config.TrieTimeout}
	)
	eth.blockchain, err = kernel.NewBlockChain(chainDb, cacheConfig, eth.chainConfig, eth.engine, vmConfig)
	if err != nil {
		return nil, err
	}
	if compat, ok := genesisErr.(*params.ConfigCompatError); ok {
		log4j.Warn("Rewinding chain to upgrade configuration", "err", compat)
		eth.blockchain.SetHead(compat.RewindTo)
		rawdb.WriteChainConfig(chainDb, genesisHash, chainConfig)
	}
	eth.bloomIndexer.Start(eth.blockchain)

	if config.TxPool.Journal != "" {
		config.TxPool.Journal = ctx.ResolvePath(config.TxPool.Journal)
	}
	eth.txPool = kernel.NewTxPool(config.TxPool, eth.chainConfig, eth.blockchain)

	if eth.protocolManager, err = NewProtocolManager(eth.chainConfig, config.SyncMode, config.NetworkId, eth.eventMux, eth.txPool, eth.engine, eth.blockchain, chainDb); err != nil {
		return nil, err
	}

	eth.miner = miner.New(eth, eth.chainConfig, eth.EventMux(), eth.engine, config.MinerRecommit, config.MinerGasFloor, config.MinerGasCeil)
	eth.miner.SetExtra(makeExtraData(config.MinerExtraData))

	eth.APIBackend = &EthAPIBackend{eth, nil}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.MinerGasPrice
	}
	eth.APIBackend.gpo = gasprice.NewOracle(eth.APIBackend, gpoParams)

	return eth, nil
}

func makeExtraData(extra []byte) []byte {
	if len(extra) == 0 {
		extra, _ = rlp.EncodeToBytes([]interface{}{
			uint(params.VersionMajor<<16 | params.VersionMinor<<8 | params.VersionPatch),
			"gcan",
			runtime.Version(),
			runtime.GOOS,
		})
	}
	if uint64(len(extra)) > params.MaximumExtraDataSize {
		log4j.Warn("Miner extra data exceed limit", "extra", hexutil.Bytes(extra), "limit", params.MaximumExtraDataSize)
		extra = nil
	}
	return extra
}

func CreateDB(ctx *node.ServiceContext, config *Config, name string) (candb.Database, error) {
	db, err := ctx.OpenDatabase(name, config.DatabaseCache, config.DatabaseHandles)
	if err != nil {
		return nil, err
	}
	if db, ok := db.(*candb.LDBDatabase); ok {
		db.Meter("eth/db/chaindata/")
	}
	return db, nil
}

func CreateConsensusEngine(ctx *node.ServiceContext, chainConfig *params.ChainConfig, config *ethash.Config, notify []string, noverify bool, db candb.Database) consensus.Engine {
	if chainConfig.Clique != nil {
		return clique.New(chainConfig.Clique, db)
	}
	switch config.PowMode {
	case ethash.ModeFake:
		log4j.Warn("Ethash used in fake mode")
		return ethash.NewFaker()
	case ethash.ModeTest:
		log4j.Warn("Ethash used in test mode")
		return ethash.NewTester(nil, noverify)
	case ethash.ModeShared:
		log4j.Warn("Ethash used in shared mode")
		return ethash.NewShared()
	default:
		engine := ethash.New(ethash.Config{
			CacheDir:       ctx.ResolvePath(config.CacheDir),
			CachesInMem:    config.CachesInMem,
			CachesOnDisk:   config.CachesOnDisk,
			DatasetDir:     config.DatasetDir,
			DatasetsInMem:  config.DatasetsInMem,
			DatasetsOnDisk: config.DatasetsOnDisk,
		}, notify, noverify)
		engine.SetThreads(-1)
		return engine
	}
}

func (s *CANChain) APIs() []rpc.API {
	apis := canapi.GetAPIs(s.APIBackend)

	apis = append(apis, s.engine.APIs(s.BlockChain())...)

	return append(apis, []rpc.API{
		{
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicEthereumAPI(s),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicMinerAPI(s),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   downloader.NewPublicDownloaderAPI(s.protocolManager.downloader, s.eventMux),
			Public:    true,
		}, {
			Namespace: "miner",
			Version:   "1.0",
			Service:   NewPrivateMinerAPI(s),
			Public:    false,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   filters.NewPublicFilterAPI(s.APIBackend, false),
			Public:    true,
		}, {
			Namespace: "admin",
			Version:   "1.0",
			Service:   NewPrivateAdminAPI(s),
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPublicDebugAPI(s),
			Public:    true,
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPrivateDebugAPI(s.chainConfig, s),
		}, {
			Namespace: "net",
			Version:   "1.0",
			Service:   s.netRPCService,
			Public:    true,
		},
	}...)
}

func (s *CANChain) ResetWithGenesisBlock(gb *types.Block) {
	s.blockchain.ResetWithGenesisBlock(gb)
}

func (s *CANChain) Canerbase() (eb common.Address, err error) {
	s.lock.RLock()
	canerbase := s.canerbase
	s.lock.RUnlock()

	if canerbase != (common.Address{}) {
		return canerbase, nil
	}
	if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			canerbase := accounts[0].Address

			s.lock.Lock()
			s.canerbase = canerbase
			s.lock.Unlock()

			log4j.Info("Canerbase automatically configured", "address", canerbase)
			return canerbase, nil
		}
	}
	return common.Address{}, fmt.Errorf("canerbase must be explicitly specified")
}

func (s *CANChain) SetCanerbase(canerbase common.Address) {
	s.lock.Lock()
	s.canerbase = canerbase
	s.lock.Unlock()

	s.miner.SetCanerbase(canerbase)
}

func (s *CANChain) StartMining(threads int) error {
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := s.engine.(threaded); ok {
		log4j.Info("Updated mining threads", "threads", threads)
		if threads == 0 {
			threads = -1
		}
		th.SetThreads(threads)
	}
	if !s.IsMining() {
		s.lock.RLock()
		price := s.gasPrice
		s.lock.RUnlock()
		s.txPool.SetGasPrice(price)

		eb, err := s.Canerbase()
		if err != nil {
			log4j.Error("Cannot start mining without canerbase", "err", err)
			return fmt.Errorf("canerbase missing: %v", err)
		}
		if clique, ok := s.engine.(*clique.Clique); ok {
			wallet, err := s.accountManager.Find(accounts.Account{Address: eb})
			if wallet == nil || err != nil {
				log4j.Error("Canerbase account unavailable locally", "err", err)
				return fmt.Errorf("signer missing: %v", err)
			}
			clique.Authorize(eb, wallet.SignHash)
		}
		atomic.StoreUint32(&s.protocolManager.acceptTxs, 1)

		go s.miner.Start(eb)
	}
	return nil
}

func (s *CANChain) StopMining() {
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := s.engine.(threaded); ok {
		th.SetThreads(-1)
	}
	s.miner.Stop()
}

func (s *CANChain) IsMining() bool      { return s.miner.Mining() }
func (s *CANChain) Miner() *miner.Miner { return s.miner }

func (s *CANChain) AccountManager() *accounts.Manager  { return s.accountManager }
func (s *CANChain) BlockChain() *kernel.BlockChain     { return s.blockchain }
func (s *CANChain) TxPool() *kernel.TxPool             { return s.txPool }
func (s *CANChain) EventMux() *event.TypeMux           { return s.eventMux }
func (s *CANChain) Engine() consensus.Engine           { return s.engine }
func (s *CANChain) ChainDb() candb.Database            { return s.chainDb }
func (s *CANChain) IsListening() bool                  { return true }
func (s *CANChain) EthVersion() int                    { return int(s.protocolManager.SubProtocols[0].Version) }
func (s *CANChain) NetVersion() uint64                 { return s.networkID }
func (s *CANChain) Downloader() *downloader.Downloader { return s.protocolManager.downloader }

func (s *CANChain) Protocols() []p2p.Protocol {
	if s.lesServer == nil {
		return s.protocolManager.SubProtocols
	}
	return append(s.protocolManager.SubProtocols, s.lesServer.Protocols()...)
}

func (s *CANChain) Start(srvr *p2p.Server) error {
	s.startBloomHandlers(params.BloomBitsBlocks)

	s.netRPCService = canapi.NewPublicNetAPI(srvr, s.NetVersion())

	maxPeers := srvr.MaxPeers
	if s.config.LightServ > 0 {
		if s.config.LightPeers >= srvr.MaxPeers {
			return fmt.Errorf("invalid peer config: light peer count (%d) >= total peer count (%d)", s.config.LightPeers, srvr.MaxPeers)
		}
		maxPeers -= s.config.LightPeers
	}
	s.protocolManager.Start(maxPeers)
	if s.lesServer != nil {
		s.lesServer.Start(srvr)
	}
	return nil
}

func (s *CANChain) Stop() error {
	s.bloomIndexer.Close()
	s.blockchain.Stop()
	s.engine.Close()
	s.protocolManager.Stop()
	if s.lesServer != nil {
		s.lesServer.Stop()
	}
	s.txPool.Stop()
	s.miner.Stop()
	s.eventMux.Stop()

	s.chainDb.Close()
	close(s.shutdownChan)
	return nil
}
