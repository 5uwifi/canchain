
package api

import (
	"crypto/ecdsa"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/contracts/ens"
	"github.com/5uwifi/canchain/basis/crypto"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/network"
	"github.com/5uwifi/canchain/basis/swarm/pss"
	"github.com/5uwifi/canchain/basis/swarm/services/swap"
	"github.com/5uwifi/canchain/basis/swarm/storage"
)

const (
	DefaultHTTPListenAddr = "127.0.0.1"
	DefaultHTTPPort       = "8500"
)

type Config struct {
	// serialised/persisted fields
	*storage.FileStoreParams
	*storage.LocalStoreParams
	*network.HiveParams
	Swap *swap.LocalProfile
	Pss  *pss.PssParams
	//*network.SyncParams
	Contract          common.Address
	EnsRoot           common.Address
	EnsAPIs           []string
	Path              string
	ListenAddr        string
	Port              string
	PublicKey         string
	BzzKey            string
	NodeID            string
	NetworkID         uint64
	SwapEnabled       bool
	SyncEnabled       bool
	DeliverySkipCheck bool
	SyncUpdateDelay   time.Duration
	SwapAPI           string
	Cors              string
	BzzAccount        string
	BootNodes         string
	privateKey        *ecdsa.PrivateKey
}

func NewConfig() (c *Config) {

	c = &Config{
		LocalStoreParams: storage.NewDefaultLocalStoreParams(),
		FileStoreParams:  storage.NewFileStoreParams(),
		HiveParams:       network.NewHiveParams(),
		//SyncParams:    network.NewDefaultSyncParams(),
		Swap:              swap.NewDefaultSwapParams(),
		Pss:               pss.NewPssParams(),
		ListenAddr:        DefaultHTTPListenAddr,
		Port:              DefaultHTTPPort,
		Path:              node.DefaultDataDir(),
		EnsAPIs:           nil,
		EnsRoot:           ens.TestNetAddress,
		NetworkID:         network.DefaultNetworkID,
		SwapEnabled:       false,
		SyncEnabled:       true,
		DeliverySkipCheck: false,
		SyncUpdateDelay:   15 * time.Second,
		SwapAPI:           "",
		BootNodes:         "",
	}

	return
}

func (c *Config) Init(prvKey *ecdsa.PrivateKey) {

	address := crypto.PubkeyToAddress(prvKey.PublicKey)
	c.Path = filepath.Join(c.Path, "bzz-"+common.Bytes2Hex(address.Bytes()))
	err := os.MkdirAll(c.Path, os.ModePerm)
	if err != nil {
		log.Error(fmt.Sprintf("Error creating root swarm data directory: %v", err))
		return
	}

	pubkey := crypto.FromECDSAPub(&prvKey.PublicKey)
	pubkeyhex := common.ToHex(pubkey)
	keyhex := crypto.Keccak256Hash(pubkey).Hex()

	c.PublicKey = pubkeyhex
	c.BzzKey = keyhex
	c.NodeID = discover.PubkeyID(&prvKey.PublicKey).String()

	if c.SwapEnabled {
		c.Swap.Init(c.Contract, prvKey)
	}

	c.privateKey = prvKey
	c.LocalStoreParams.Init(c.Path)
	c.LocalStoreParams.BaseKey = common.FromHex(keyhex)

	c.Pss = c.Pss.WithPrivateKey(c.privateKey)
}

func (c *Config) ShiftPrivateKey() (privKey *ecdsa.PrivateKey) {
	if c.privateKey != nil {
		privKey = c.privateKey
		c.privateKey = nil
	}
	return privKey
}
