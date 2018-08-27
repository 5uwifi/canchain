package main

import (
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/params"
)

var daoOldGenesis = `{
	"alloc"      : {},
	"coinbase"   : "0x0000000000000000000000000000000000000000",
	"difficulty" : "0x20000",
	"extraData"  : "",
	"gasLimit"   : "0x2fefd8",
	"nonce"      : "0x0000000000000042",
	"mixhash"    : "0x0000000000000000000000000000000000000000000000000000000000000000",
	"parentHash" : "0x0000000000000000000000000000000000000000000000000000000000000000",
	"timestamp"  : "0x00",
	"config"     : {}
}`

var daoNoForkGenesis = `{
	"alloc"      : {},
	"coinbase"   : "0x0000000000000000000000000000000000000000",
	"difficulty" : "0x20000",
	"extraData"  : "",
	"gasLimit"   : "0x2fefd8",
	"nonce"      : "0x0000000000000042",
	"mixhash"    : "0x0000000000000000000000000000000000000000000000000000000000000000",
	"parentHash" : "0x0000000000000000000000000000000000000000000000000000000000000000",
	"timestamp"  : "0x00",
	"config"     : {
		"daoForkBlock"   : 314,
		"daoForkSupport" : false
	}
}`

var daoProForkGenesis = `{
	"alloc"      : {},
	"coinbase"   : "0x0000000000000000000000000000000000000000",
	"difficulty" : "0x20000",
	"extraData"  : "",
	"gasLimit"   : "0x2fefd8",
	"nonce"      : "0x0000000000000042",
	"mixhash"    : "0x0000000000000000000000000000000000000000000000000000000000000000",
	"parentHash" : "0x0000000000000000000000000000000000000000000000000000000000000000",
	"timestamp"  : "0x00",
	"config"     : {
		"daoForkBlock"   : 314,
		"daoForkSupport" : true
	}
}`

var daoGenesisHash = common.HexToHash("5e1fc79cb4ffa4739177b5408045cd5d51c6cf766133f23f7cd72ee1f8d790e0")
var daoGenesisForkBlock = big.NewInt(314)

func TestDAOForkBlockNewChain(t *testing.T) {
	for i, arg := range []struct {
		genesis     string
		expectBlock *big.Int
		expectVote  bool
	}{
		{"", params.MainnetChainConfig.DAOForkBlock, false},
		{daoOldGenesis, nil, false},
		{daoNoForkGenesis, daoGenesisForkBlock, false},
		{daoProForkGenesis, daoGenesisForkBlock, true},
	} {
		testDAOForkBlockNewChain(t, i, arg.genesis, arg.expectBlock, arg.expectVote)
	}
}

func testDAOForkBlockNewChain(t *testing.T, test int, genesis string, expectBlock *big.Int, expectVote bool) {
	datadir := tmpdir(t)
	defer os.RemoveAll(datadir)

	if genesis != "" {
		json := filepath.Join(datadir, "genesis.json")
		if err := ioutil.WriteFile(json, []byte(genesis), 0600); err != nil {
			t.Fatalf("test %d: failed to write genesis file: %v", test, err)
		}
		runGcan(t, "--datadir", datadir, "init", json).WaitExit()
	} else {
		args := []string{"--port", "0", "--maxpeers", "0", "--nodiscover", "--nat", "none", "--ipcdisable", "--datadir", datadir}
		gcan := runGcan(t, append(args, []string{"--exec", "2+2", "console"}...)...)
		gcan.WaitExit()
	}
	path := filepath.Join(datadir, "gcan", "chaindata")
	db, err := candb.NewLDBDatabase(path, 0, 0)
	if err != nil {
		t.Fatalf("test %d: failed to open test database: %v", test, err)
	}
	defer db.Close()

	genesisHash := common.HexToHash("0x2c5a0541007aa03d34071f9b5a1e8057fdb7b87313935b61674cedf0978bc095")
	if genesis != "" {
		genesisHash = daoGenesisHash
	}
	config := rawdb.ReadChainConfig(db, genesisHash)
	if config == nil {
		t.Errorf("test %d: failed to retrieve chain config: %v", test, err)
		return
	}
	if config.DAOForkBlock == nil {
		if expectBlock != nil {
			t.Errorf("test %d: dao hard-fork block mismatch: have nil, want %v", test, expectBlock)
		}
	} else if expectBlock == nil {
		t.Errorf("test %d: dao hard-fork block mismatch: have %v, want nil", test, config.DAOForkBlock)
	} else if config.DAOForkBlock.Cmp(expectBlock) != 0 {
		t.Errorf("test %d: dao hard-fork block mismatch: have %v, want %v", test, config.DAOForkBlock, expectBlock)
	}
	if config.DAOForkSupport != expectVote {
		t.Errorf("test %d: dao hard-fork support mismatch: have %v, want %v", test, config.DAOForkSupport, expectVote)
	}
}
