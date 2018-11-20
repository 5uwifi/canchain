// +build none

package main

import (
	"crypto/ecdsa"
	"io/ioutil"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/5uwifi/canchain/accounts/keystore"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/fdlimit"
	"github.com/5uwifi/canchain/lib/consensus/ethash"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/can"
	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/ccnode"
	"github.com/5uwifi/canchain/params"
)

func main() {
	log4j.Root().SetHandler(log4j.LvlFilterHandler(log4j.LvlInfo, log4j.StreamHandler(os.Stderr, log4j.TerminalFormat(true))))
	fdlimit.Raise(2048)

	faucets := make([]*ecdsa.PrivateKey, 128)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}
	ethash.MakeDataset(1, filepath.Join(os.Getenv("HOME"), ".cchash"))

	genesis := makeGenesis(faucets)

	var (
		nodes  []*node.Node
		enodes []*ccnode.Node
	)
	for i := 0; i < 4; i++ {
		node, err := makeMiner(genesis)
		if err != nil {
			panic(err)
		}
		defer node.Stop()

		for node.Server().NodeInfo().Ports.Listener == 0 {
			time.Sleep(250 * time.Millisecond)
		}
		for _, n := range enodes {
			node.Server().AddPeer(n)
		}
		nodes = append(nodes, node)
		enodes = append(enodes, node.Server().Self())

		store := node.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
		if _, err := store.NewAccount(""); err != nil {
			panic(err)
		}
	}
	time.Sleep(3 * time.Second)

	for _, node := range nodes {
		var canchain *can.CANChain
		if err := node.Service(&canchain); err != nil {
			panic(err)
		}
		if err := canchain.StartMining(1); err != nil {
			panic(err)
		}
	}
	time.Sleep(3 * time.Second)

	nonces := make([]uint64, len(faucets))
	for {
		index := rand.Intn(len(faucets))

		var canchain *can.CANChain
		if err := nodes[index%len(nodes)].Service(&canchain); err != nil {
			panic(err)
		}
		tx, err := types.SignTx(types.NewTransaction(nonces[index], crypto.PubkeyToAddress(faucets[index].PublicKey), new(big.Int), 21000, big.NewInt(100000000000+rand.Int63n(65536)), nil), types.HomesteadSigner{}, faucets[index])
		if err != nil {
			panic(err)
		}
		if err := canchain.TxPool().AddLocal(tx); err != nil {
			panic(err)
		}
		nonces[index]++

		if pend, _ := canchain.TxPool().Stats(); pend > 2048 {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func makeGenesis(faucets []*ecdsa.PrivateKey) *kernel.Genesis {
	genesis := kernel.DefaultIronmanGenesisBlock()
	genesis.Difficulty = params.MinimumDifficulty
	genesis.GasLimit = 25000000

	genesis.Config.ChainID = big.NewInt(18)
	genesis.Config.EIP150Hash = common.Hash{}

	genesis.Alloc = kernel.GenesisAlloc{}
	for _, faucet := range faucets {
		genesis.Alloc[crypto.PubkeyToAddress(faucet.PublicKey)] = kernel.GenesisAccount{
			Balance: new(big.Int).Exp(big.NewInt(2), big.NewInt(128), nil),
		}
	}
	return genesis
}

func makeMiner(genesis *kernel.Genesis) (*node.Node, error) {
	datadir, _ := ioutil.TempDir("", "")

	config := &node.Config{
		Name:    "gcan",
		Version: params.Version,
		DataDir: datadir,
		P2P: p2p.Config{
			ListenAddr:  "0.0.0.0:0",
			NoDiscovery: true,
			MaxPeers:    25,
		},
		NoUSB:             true,
		UseLightweightKDF: true,
	}
	stack, err := node.New(config)
	if err != nil {
		return nil, err
	}
	if err := stack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
		return can.New(ctx, &can.Config{
			Genesis:         genesis,
			NetworkId:       genesis.Config.ChainID.Uint64(),
			SyncMode:        downloader.FullSync,
			DatabaseCache:   256,
			DatabaseHandles: 256,
			TxPool:          kernel.DefaultTxPoolConfig,
			GPO:             can.DefaultConfig.GPO,
			Ethash:          can.DefaultConfig.Ethash,
			MinerGasFloor:   genesis.GasLimit * 9 / 10,
			MinerGasCeil:    genesis.GasLimit * 11 / 10,
			MinerGasPrice:   big.NewInt(1),
			MinerRecommit:   time.Second,
		})
	}); err != nil {
		return nil, err
	}
	return stack, stack.Start()
}
