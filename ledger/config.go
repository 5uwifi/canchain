//
// (at your option) any later version.
//
//

package ledger

import (
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/consensus/canhash"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/ledger/downloader"
	"github.com/5uwifi/canchain/ledger/feeprice"
	"github.com/5uwifi/canchain/params"
)

var DefaultConfig = Config{
	SyncMode: downloader.FastSync,
	Canhash: canhash.Config{
		CacheDir:       "canhash",
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
	FeePrice:      big.NewInt(18 * params.Shannon),

	TxPool: kernel.DefaultTxPoolConfig,
	GPO: feeprice.Config{
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
		DefaultConfig.Canhash.DatasetDir = filepath.Join(home, "AppData", "Canhash")
	} else {
		DefaultConfig.Canhash.DatasetDir = filepath.Join(home, ".canhash")
	}
}


type Config struct {
	// The genesis block, which is inserted if the database is empty.
	// If nil, the Ethereum main net block is used.
	Genesis *kernel.Genesis `toml:",omitempty"`

	// Protocol options
	NetworkId uint64 // Network ID to use for selecting peers to connect to
	SyncMode  downloader.SyncMode
	NoPruning bool

	// Light client options
	LightServ  int `toml:",omitempty"` // Maximum percentage of time allowed for serving LES requests
	LightPeers int `toml:",omitempty"` // Maximum number of LES client peers

	// Database options
	SkipBcVersionCheck bool `toml:"-"`
	DatabaseHandles    int  `toml:"-"`
	DatabaseCache      int
	TrieCache          int
	TrieTimeout        time.Duration

	// Mining-related options
	Canerbase    common.Address `toml:",omitempty"`
	MinerThreads int            `toml:",omitempty"`
	ExtraData    []byte         `toml:",omitempty"`
	FeePrice     *big.Int

	// Ethash options
	Canhash canhash.Config

	// Transaction pool options
	TxPool kernel.TxPoolConfig

	// Gas Price Oracle options
	GPO feeprice.Config

	// Enables tracking of SHA3 preimages in the VM
	EnablePreimageRecording bool

	// Miscellaneous options
	DocRoot string `toml:"-"`
}

type configMarshaling struct {
	ExtraData hexutil.Bytes
}
