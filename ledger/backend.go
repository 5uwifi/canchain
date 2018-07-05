//
// (at your option) any later version.
//
//

package ledger

import (
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/consensus"
	"github.com/5uwifi/canchain/consensus/clique"
	"github.com/5uwifi/canchain/consensus/canhash"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/bloombits"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/ledger/downloader"
	"github.com/5uwifi/canchain/ledger/filters"
	"github.com/5uwifi/canchain/ledger/feeprice"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/basis/event"
	"github.com/5uwifi/canchain/internal/canapi"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/miner"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/basis/rlp"
	"github.com/5uwifi/canchain/rpc"
)

type LesServer interface {
	Start(srvr *p2p.Server)
	Stop()
	Protocols() []p2p.Protocol
	SetBloomBitsIndexer(bbIndexer *kernel.ChainIndexer)
}

type Ledger struct {
	config      *Config
	chainConfig *params.ChainConfig

	// Channel for shutting down the service
	shutdownChan chan bool // Channel for shutting down the Ethereum

	// Handlers
	txPool          *kernel.TxPool
	blockchain      *kernel.BlockChain
	protocolManager *ProtocolManager
	lesServer       LesServer

	// DB interfaces
	chainDb candb.Database // Block chain database

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	bloomRequests chan chan *bloombits.Retrieval // Channel receiving bloom data retrieval requests
	bloomIndexer  *kernel.ChainIndexer           // Bloom indexer operating during block imports

	APIBackend *EthAPIBackend

	miner     *miner.Miner
	gasPrice  *big.Int
	etherbase common.Address

	networkID     uint64
	netRPCService *canapi.PublicNetAPI

	lock sync.RWMutex // Protects the variadic fields (e.g. gas price and etherbase)
}

func (s *Ledger) AddLesServer(ls LesServer) {
	s.lesServer = ls
	ls.SetBloomBitsIndexer(s.bloomIndexer)
}

func New(ctx *node.ServiceContext, config *Config) (*Ledger, error) {
	if config.SyncMode == downloader.LightSync {
		return nil, errors.New("can't run ledger.Ledger in light sync mode, use les.LightEthereum")
	}
	if !config.SyncMode.IsValid() {
		return nil, fmt.Errorf("invalid sync mode %d", config.SyncMode)
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

	eth := &Ledger{
		config:         config,
		chainDb:        chainDb,
		chainConfig:    chainConfig,
		eventMux:       ctx.EventMux,
		accountManager: ctx.AccountManager,
		engine:         CreateConsensusEngine(ctx, &config.Canhash, chainConfig, chainDb),
		shutdownChan:   make(chan bool),
		networkID:      config.NetworkId,
		gasPrice:       config.FeePrice,
		etherbase:      config.Canerbase,
		bloomRequests:  make(chan chan *bloombits.Retrieval),
		bloomIndexer:   NewBloomIndexer(chainDb, params.BloomBitsBlocks),
	}

	log4j.Info("Initialising CANChain protocol", "versions", ProtocolVersions, "network", config.NetworkId)

	if !config.SkipBcVersionCheck {
		bcVersion := rawdb.ReadDatabaseVersion(chainDb)
		if bcVersion != kernel.BlockChainVersion && bcVersion != 0 {
			return nil, fmt.Errorf("Blockchain DB version mismatch (%d / %d). Run geth upgradedb.\n", bcVersion, kernel.BlockChainVersion)
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
	// Rewind the chain in case of an incompatible config upgrade.
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
	eth.miner = miner.New(eth, eth.chainConfig, eth.EventMux(), eth.engine)
	eth.miner.SetExtra(makeExtraData(config.ExtraData))

	eth.APIBackend = &EthAPIBackend{eth, nil}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.FeePrice
	}
	eth.APIBackend.gpo = feeprice.NewOracle(eth.APIBackend, gpoParams)

	return eth, nil
}

func makeExtraData(extra []byte) []byte {
	if len(extra) == 0 {
		// create default extradata
		extra, _ = rlp.EncodeToBytes([]interface{}{
			uint(params.VersionMajor<<16 | params.VersionMinor<<8 | params.VersionPatch),
			"geth",
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
		db.Meter("ledger/db/chaindata/")
	}
	return db, nil
}

func CreateConsensusEngine(ctx *node.ServiceContext, config *canhash.Config, chainConfig *params.ChainConfig, db candb.Database) consensus.Engine {
	// If proof-of-authority is requested, set it up
	if chainConfig.Clique != nil {
		return clique.New(chainConfig.Clique, db)
	}
	// Otherwise assume proof-of-work
	switch config.PowMode {
	case canhash.ModeFake:
		log4j.Warn("Ethash used in fake mode")
		return canhash.NewFaker()
	case canhash.ModeTest:
		log4j.Warn("Ethash used in test mode")
		return canhash.NewTester()
	case canhash.ModeShared:
		log4j.Warn("Ethash used in shared mode")
		return canhash.NewShared()
	default:
		engine := canhash.New(canhash.Config{
			CacheDir:       ctx.ResolvePath(config.CacheDir),
			CachesInMem:    config.CachesInMem,
			CachesOnDisk:   config.CachesOnDisk,
			DatasetDir:     config.DatasetDir,
			DatasetsInMem:  config.DatasetsInMem,
			DatasetsOnDisk: config.DatasetsOnDisk,
		})
		engine.SetThreads(-1) // Disable CPU mining
		return engine
	}
}

func (s *Ledger) APIs() []rpc.API {
	apis := canapi.GetAPIs(s.APIBackend)

	// Append any APIs exposed explicitly by the consensus engine
	apis = append(apis, s.engine.APIs(s.BlockChain())...)

	// Append all the local APIs and return
	return append(apis, []rpc.API{
		{
			Namespace: "can",
			Version:   "1.0",
			Service:   NewPublicEthereumAPI(s),
			Public:    true,
		}, {
			Namespace: "can",
			Version:   "1.0",
			Service:   NewPublicMinerAPI(s),
			Public:    true,
		}, {
			Namespace: "can",
			Version:   "1.0",
			Service:   downloader.NewPublicDownloaderAPI(s.protocolManager.downloader, s.eventMux),
			Public:    true,
		}, {
			Namespace: "miner",
			Version:   "1.0",
			Service:   NewPrivateMinerAPI(s),
			Public:    false,
		}, {
			Namespace: "can",
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

func (s *Ledger) ResetWithGenesisBlock(gb *types.Block) {
	s.blockchain.ResetWithGenesisBlock(gb)
}

func (s *Ledger) Etherbase() (eb common.Address, err error) {
	s.lock.RLock()
	etherbase := s.etherbase
	s.lock.RUnlock()

	if etherbase != (common.Address{}) {
		return etherbase, nil
	}
	if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			etherbase := accounts[0].Address

			s.lock.Lock()
			s.etherbase = etherbase
			s.lock.Unlock()

			log4j.Info("Etherbase automatically configured", "address", etherbase)
			return etherbase, nil
		}
	}
	return common.Address{}, fmt.Errorf("etherbase must be explicitly specified")
}

func (s *Ledger) SetEtherbase(etherbase common.Address) {
	s.lock.Lock()
	s.etherbase = etherbase
	s.lock.Unlock()

	s.miner.SetEtherbase(etherbase)
}

func (s *Ledger) StartMining(local bool) error {
	eb, err := s.Etherbase()
	if err != nil {
		log4j.Error("Cannot start mining without etherbase", "err", err)
		return fmt.Errorf("etherbase missing: %v", err)
	}
	if clique, ok := s.engine.(*clique.Clique); ok {
		wallet, err := s.accountManager.Find(accounts.Account{Address: eb})
		if wallet == nil || err != nil {
			log4j.Error("Etherbase account unavailable locally", "err", err)
			return fmt.Errorf("signer missing: %v", err)
		}
		clique.Authorize(eb, wallet.SignHash)
	}
	if local {
		// If local (CPU) mining is started, we can disable the transaction rejection
		// mechanism introduced to speed sync times. CPU mining on mainnet is ludicrous
		// so none will ever hit this path, whereas marking sync done on CPU mining
		// will ensure that private networks work in single miner mode too.
		atomic.StoreUint32(&s.protocolManager.acceptTxs, 1)
	}
	go s.miner.Start(eb)
	return nil
}

func (s *Ledger) StopMining()         { s.miner.Stop() }
func (s *Ledger) IsMining() bool      { return s.miner.Mining() }
func (s *Ledger) Miner() *miner.Miner { return s.miner }

func (s *Ledger) AccountManager() *accounts.Manager { return s.accountManager }
func (s *Ledger) BlockChain() *kernel.BlockChain    { return s.blockchain }
func (s *Ledger) TxPool() *kernel.TxPool            { return s.txPool }
func (s *Ledger) EventMux() *event.TypeMux          { return s.eventMux }
func (s *Ledger) Engine() consensus.Engine          { return s.engine }
func (s *Ledger) ChainDb() candb.Database           { return s.chainDb }
func (s *Ledger) IsListening() bool                 { return true } // Always listening
func (s *Ledger) EthVersion() int                   { return int(s.protocolManager.SubProtocols[0].Version) }
func (s *Ledger) NetVersion() uint64                { return s.networkID }
func (s *Ledger) Downloader() *downloader.Downloader { return s.protocolManager.downloader }

func (s *Ledger) Protocols() []p2p.Protocol {
	if s.lesServer == nil {
		return s.protocolManager.SubProtocols
	}
	return append(s.protocolManager.SubProtocols, s.lesServer.Protocols()...)
}

func (s *Ledger) Start(srvr *p2p.Server) error {
	// Start the bloom bits servicing goroutines
	s.startBloomHandlers()

	// Start the RPC service
	s.netRPCService = canapi.NewPublicNetAPI(srvr, s.NetVersion())

	// Figure out a max peers count based on the server limits
	maxPeers := srvr.MaxPeers
	if s.config.LightServ > 0 {
		if s.config.LightPeers >= srvr.MaxPeers {
			return fmt.Errorf("invalid peer config: light peer count (%d) >= total peer count (%d)", s.config.LightPeers, srvr.MaxPeers)
		}
		maxPeers -= s.config.LightPeers
	}
	// Start the networking layer and the light server if requested
	s.protocolManager.Start(maxPeers)
	if s.lesServer != nil {
		s.lesServer.Start(srvr)
	}
	return nil
}

func (s *Ledger) Stop() error {
	s.bloomIndexer.Close()
	s.blockchain.Stop()
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
