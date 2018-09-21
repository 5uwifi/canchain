package clique

import (
	"bytes"
	"crypto/ecdsa"
	"sort"
	"testing"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/params"
)

type testerAccountPool struct {
	accounts map[string]*ecdsa.PrivateKey
}

func newTesterAccountPool() *testerAccountPool {
	return &testerAccountPool{
		accounts: make(map[string]*ecdsa.PrivateKey),
	}
}

func (ap *testerAccountPool) checkpoint(header *types.Header, signers []string) {
	auths := make([]common.Address, len(signers))
	for i, signer := range signers {
		auths[i] = ap.address(signer)
	}
	sort.Sort(signersAscending(auths))
	for i, auth := range auths {
		copy(header.Extra[extraVanity+i*common.AddressLength:], auth.Bytes())
	}
}

func (ap *testerAccountPool) address(account string) common.Address {
	if account == "" {
		return common.Address{}
	}
	if ap.accounts[account] == nil {
		ap.accounts[account], _ = crypto.GenerateKey()
	}
	return crypto.PubkeyToAddress(ap.accounts[account].PublicKey)
}

func (ap *testerAccountPool) sign(header *types.Header, signer string) {
	if ap.accounts[signer] == nil {
		ap.accounts[signer], _ = crypto.GenerateKey()
	}
	sig, _ := crypto.Sign(sigHash(header).Bytes(), ap.accounts[signer])
	copy(header.Extra[len(header.Extra)-extraSeal:], sig)
}

type testerVote struct {
	signer     string
	voted      string
	auth       bool
	checkpoint []string
	newbatch   bool
}

func TestClique(t *testing.T) {
	tests := []struct {
		epoch   uint64
		signers []string
		votes   []testerVote
		results []string
		failure error
	}{
		{
			signers: []string{"A"},
			votes:   []testerVote{{signer: "A"}},
			results: []string{"A"},
		}, {
			signers: []string{"A"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: true},
				{signer: "B"},
				{signer: "A", voted: "C", auth: true},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B", voted: "C", auth: true},
				{signer: "A", voted: "D", auth: true},
				{signer: "B", voted: "D", auth: true},
				{signer: "C"},
				{signer: "A", voted: "E", auth: true},
				{signer: "B", voted: "E", auth: true},
			},
			results: []string{"A", "B", "C", "D"},
		}, {
			signers: []string{"A"},
			votes: []testerVote{
				{signer: "A", voted: "A", auth: false},
			},
			results: []string{},
		}, {
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: false},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: false},
				{signer: "B", voted: "B", auth: false},
			},
			results: []string{"A"},
		}, {
			signers: []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
			},
			results: []string{"A", "B", "C", "D"},
		}, {
			signers: []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "D", auth: false},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
			},
			results: []string{"A", "B", "C"},
		}, {
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A", voted: "C", auth: true},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A", voted: "D", auth: true},
				{signer: "B"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: true},
				{signer: "A"},
				{signer: "B", voted: "C", auth: true},
			},
			results: []string{"A", "B", "C", "D"},
		}, {
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "B", auth: false},
				{signer: "B"},
				{signer: "A", voted: "B", auth: false},
				{signer: "B"},
				{signer: "A", voted: "B", auth: false},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
				{signer: "A"},
				{signer: "B", voted: "C", auth: false},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "C", voted: "B", auth: false},
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "A", voted: "B", auth: false},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "C", voted: "B", auth: false},
				{signer: "A", voted: "C", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "A", voted: "B", auth: false},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
			},
			results: []string{"A", "B", "C"},
		}, {
			signers: []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
				{signer: "A"},
				{signer: "C", voted: "C", auth: true},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A", "B", "C", "D"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: false},
				{signer: "B"},
				{signer: "C"},
				{signer: "A", voted: "D", auth: false},
				{signer: "B", voted: "C", auth: false},
				{signer: "C"},
				{signer: "A"},
				{signer: "B", voted: "D", auth: false},
				{signer: "C", voted: "D", auth: false},
				{signer: "A"},
				{signer: "B", voted: "C", auth: true},
			},
			results: []string{"A", "B", "C"},
		}, {
			signers: []string{"A", "B", "C", "D", "E"},
			votes: []testerVote{
				{signer: "A", voted: "F", auth: true},
				{signer: "B", voted: "F", auth: true},
				{signer: "C", voted: "F", auth: true},
				{signer: "D", voted: "F", auth: false},
				{signer: "E", voted: "F", auth: false},
				{signer: "B", voted: "F", auth: false},
				{signer: "C", voted: "F", auth: false},
				{signer: "D", voted: "F", auth: true},
				{signer: "E", voted: "F", auth: true},
				{signer: "B", voted: "A", auth: false},
				{signer: "C", voted: "A", auth: false},
				{signer: "D", voted: "A", auth: false},
				{signer: "B", voted: "F", auth: true},
			},
			results: []string{"B", "C", "D", "E", "F"},
		}, {
			epoch:   3,
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A", voted: "C", auth: true},
				{signer: "B"},
				{signer: "A", checkpoint: []string{"A", "B"}},
				{signer: "B", voted: "C", auth: true},
			},
			results: []string{"A", "B"},
		}, {
			signers: []string{"A"},
			votes: []testerVote{
				{signer: "B"},
			},
			failure: errUnauthorizedSigner,
		}, {
			signers: []string{"A", "B"},
			votes: []testerVote{
				{signer: "A"},
				{signer: "A"},
			},
			failure: errRecentlySigned,
		}, {
			epoch:   3,
			signers: []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "A"},
				{signer: "B"},
				{signer: "A", checkpoint: []string{"A", "B", "C"}},
				{signer: "A"},
			},
			failure: errRecentlySigned,
		}, {
			epoch:   3,
			signers: []string{"A", "B", "C"},
			votes: []testerVote{
				{signer: "A"},
				{signer: "B"},
				{signer: "A", checkpoint: []string{"A", "B", "C"}},
				{signer: "A", newbatch: true},
			},
			failure: errRecentlySigned,
		},
	}
	for i, tt := range tests {
		accounts := newTesterAccountPool()

		signers := make([]common.Address, len(tt.signers))
		for j, signer := range tt.signers {
			signers[j] = accounts.address(signer)
		}
		for j := 0; j < len(signers); j++ {
			for k := j + 1; k < len(signers); k++ {
				if bytes.Compare(signers[j][:], signers[k][:]) > 0 {
					signers[j], signers[k] = signers[k], signers[j]
				}
			}
		}
		genesis := &kernel.Genesis{
			ExtraData: make([]byte, extraVanity+common.AddressLength*len(signers)+extraSeal),
		}
		for j, signer := range signers {
			copy(genesis.ExtraData[extraVanity+j*common.AddressLength:], signer[:])
		}
		db := candb.NewMemDatabase()
		genesis.Commit(db)

		config := *params.TestChainConfig
		config.Clique = &params.CliqueConfig{
			Period: 1,
			Epoch:  tt.epoch,
		}
		engine := New(config.Clique, db)
		engine.fakeDiff = true

		blocks, _ := kernel.GenerateChain(&config, genesis.ToBlock(db), engine, db, len(tt.votes), func(j int, gen *kernel.BlockGen) {
			gen.SetCoinbase(accounts.address(tt.votes[j].voted))
			if tt.votes[j].auth {
				var nonce types.BlockNonce
				copy(nonce[:], nonceAuthVote)
				gen.SetNonce(nonce)
			}
		})
		for j, block := range blocks {
			header := block.Header()
			if j > 0 {
				header.ParentHash = blocks[j-1].Hash()
			}
			header.Extra = make([]byte, extraVanity+extraSeal)
			if auths := tt.votes[j].checkpoint; auths != nil {
				header.Extra = make([]byte, extraVanity+len(auths)*common.AddressLength+extraSeal)
				accounts.checkpoint(header, auths)
			}
			header.Difficulty = diffInTurn

			accounts.sign(header, tt.votes[j].signer)
			blocks[j] = block.WithSeal(header)
		}
		batches := [][]*types.Block{nil}
		for j, block := range blocks {
			if tt.votes[j].newbatch {
				batches = append(batches, nil)
			}
			batches[len(batches)-1] = append(batches[len(batches)-1], block)
		}
		chain, err := kernel.NewBlockChain(db, nil, &config, engine, vm.Config{}, nil)
		if err != nil {
			t.Errorf("test %d: failed to create test chain: %v", i, err)
			continue
		}
		failed := false
		for j := 0; j < len(batches)-1; j++ {
			if k, err := chain.InsertChain(batches[j]); err != nil {
				t.Errorf("test %d: failed to import batch %d, block %d: %v", i, j, k, err)
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		if _, err = chain.InsertChain(batches[len(batches)-1]); err != tt.failure {
			t.Errorf("test %d: failure mismatch: have %v, want %v", i, err, tt.failure)
		}
		if tt.failure != nil {
			continue
		}
		head := blocks[len(blocks)-1]

		snap, err := engine.snapshot(chain, head.NumberU64(), head.Hash(), nil)
		if err != nil {
			t.Errorf("test %d: failed to retrieve voting snapshot: %v", i, err)
			continue
		}
		signers = make([]common.Address, len(tt.results))
		for j, signer := range tt.results {
			signers[j] = accounts.address(signer)
		}
		for j := 0; j < len(signers); j++ {
			for k := j + 1; k < len(signers); k++ {
				if bytes.Compare(signers[j][:], signers[k][:]) > 0 {
					signers[j], signers[k] = signers[k], signers[j]
				}
			}
		}
		result := snap.signers()
		if len(result) != len(signers) {
			t.Errorf("test %d: signers mismatch: have %x, want %x", i, result, signers)
			continue
		}
		for j := 0; j < len(result); j++ {
			if !bytes.Equal(result[j][:], signers[j][:]) {
				t.Errorf("test %d, signer %d: signer mismatch: have %x, want %x", i, j, result[j], signers[j])
			}
		}
	}
}
