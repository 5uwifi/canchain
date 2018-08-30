// +build none

package main

import (
	"bytes"
	"crypto/ecdsa"
	"fmt"
	"io/ioutil"
	"math/big"
	"math/rand"
	"os"
	"time"

	"github.com/5uwifi/canchain/accounts/keystore"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/fdlimit"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/can"
	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/params"
)

func main() {
	log4j.Root().SetHandler(log4j.LvlFilterHandler(log4j.LvlInfo, log4j.StreamHandler(os.Stderr, log4j.TerminalFormat(true))))
	fdlimit.Raise(2048)

	faucets := make([]*ecdsa.PrivateKey, 128)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}
	sealers := make([]*ecdsa.PrivateKey, 4)
	for i := 0; i < len(sealers); i++ {
		sealers[i], _ = crypto.GenerateKey()
	}
	genesis := makeGenesis(faucets, sealers)

	var (
		nodes  []*node.Node
		enodes []string
	)
	for _, sealer := range sealers {
		node, err := makeSealer(genesis, enodes)
		if err != nil {
			panic(err)
		}
		defer node.Stop()

		for node.Server().NodeInfo().Ports.Listener == 0 {
			time.Sleep(250 * time.Millisecond)
		}
		for _, ccnode := range enodes {
			ccnode, err := discover.ParseNode(ccnode)
			if err != nil {
				panic(err)
			}
			node.Server().AddPeer(ccnode)
		}
		nodes = append(nodes, node)

		ccnode := fmt.Sprintf("ccnode://%s@127.0.0.1:%d", node.Server().NodeInfo().ID, node.Server().NodeInfo().Ports.Listener)
		enodes = append(enodes, ccnode)

		store := node.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
		signer, err := store.ImportECDSA(sealer, "")
		if err != nil {
			panic(err)
		}
		if err := store.Unlock(signer, ""); err != nil {
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
		tx, err := types.SignTx(types.NewTransaction(nonces[index], crypto.PubkeyToAddress(faucets[index].PublicKey), new(big.Int), 21000, big.NewInt(100000000000), nil), types.HomesteadSigner{}, faucets[index])
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

func makeGenesis(faucets []*ecdsa.PrivateKey, sealers []*ecdsa.PrivateKey) *kernel.Genesis {
	genesis := kernel.DefaultTestnetGenesisBlock()
	genesis.GasLimit = 25000000

	genesis.Config.ChainID = big.NewInt(18)
	genesis.Config.Clique.Period = 1
	genesis.Config.EIP150Hash = common.Hash{}

	genesis.Alloc = kernel.GenesisAlloc{}
	for _, faucet := range faucets {
		genesis.Alloc[crypto.PubkeyToAddress(faucet.PublicKey)] = kernel.GenesisAccount{
			Balance: new(big.Int).Exp(big.NewInt(2), big.NewInt(128), nil),
		}
	}
	signers := make([]common.Address, len(sealers))
	for i, sealer := range sealers {
		signers[i] = crypto.PubkeyToAddress(sealer.PublicKey)
	}
	for i := 0; i < len(signers); i++ {
		for j := i + 1; j < len(signers); j++ {
			if bytes.Compare(signers[i][:], signers[j][:]) > 0 {
				signers[i], signers[j] = signers[j], signers[i]
			}
		}
	}
	genesis.ExtraData = make([]byte, 32+len(signers)*common.AddressLength+65)
	for i, signer := range signers {
		copy(genesis.ExtraData[32+i*common.AddressLength:], signer[:])
	}
	return genesis
}

func makeSealer(genesis *kernel.Genesis, nodes []string) (*node.Node, error) {
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
		NoUSB: true,
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
