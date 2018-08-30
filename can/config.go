package can

import (
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/can/gasprice"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/params"
)

var DefaultConfig = Config{
	SyncMode: downloader.FastSync,
	Ethash: ethash.Config{
		CacheDir:       "cchash",
		CachesInMem:    2,
		CachesOnDisk:   3,
		DatasetsInMem:  1,
		DatasetsOnDisk: 2,
	},
	NetworkId:     1,
	LightPeers:    100,
	DatabaseCache: 768,
	TrieCache:     256,
	TrieTimeout:   60 * time.Minute,
	MinerGasFloor: 8000000,
	MinerGasCeil:  8000000,
	MinerGasPrice: big.NewInt(params.GWei),
	MinerRecommit: 3 * time.Second,

	TxPool: kernel.DefaultTxPoolConfig,
	GPO: gasprice.Config{
		Blocks:     20,
		Percentile: 60,
	},
}

func init() {
	home := os.Getenv("HOME")
	if home == "" {
		if user, err := user.Current(); err == nil {
			home = user.HomeDir
		}
	}
	if runtime.GOOS == "windows" {
		DefaultConfig.Ethash.DatasetDir = filepath.Join(home, "AppData", "CChash")
	} else {
		DefaultConfig.Ethash.DatasetDir = filepath.Join(home, ".cchash")
	}
}

//go:generate gencodec -type Config -field-override configMarshaling -formats toml -out gen_config.go

type Config struct {
	Genesis *kernel.Genesis `toml:",omitempty"`

	NetworkId uint64
	SyncMode  downloader.SyncMode
	NoPruning bool

	LightServ  int `toml:",omitempty"`
	LightPeers int `toml:",omitempty"`

	SkipBcVersionCheck bool `toml:"-"`
	DatabaseHandles    int  `toml:"-"`
	DatabaseCache      int
	TrieCache          int
	TrieTimeout        time.Duration

	Canerbase      common.Address `toml:",omitempty"`
	MinerNotify    []string       `toml:",omitempty"`
	MinerExtraData []byte         `toml:",omitempty"`
	MinerGasFloor  uint64
	MinerGasCeil   uint64
	MinerGasPrice  *big.Int
	MinerRecommit  time.Duration
	MinerNoverify  bool

	Ethash ethash.Config

	TxPool kernel.TxPoolConfig

	GPO gasprice.Config

	EnablePreimageRecording bool

	DocRoot string `toml:"-"`
}

type configMarshaling struct {
	MinerExtraData hexutil.Bytes
}
