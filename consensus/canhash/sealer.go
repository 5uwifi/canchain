//
// (at your option) any later version.
//
//

package canhash

import (
	crand "crypto/rand"
	"math"
	"math/big"
	"math/rand"
	"runtime"
	"sync"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/consensus"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/basis/log4j"
)

func (canhash *Canhash) Seal(chain consensus.ChainReader, block *types.Block, stop <-chan struct{}) (*types.Block, error) {
	// If we're running a fake PoW, simply return a 0 nonce immediately
	if canhash.config.PowMode == ModeFake || canhash.config.PowMode == ModeFullFake {
		header := block.Header()
		header.Nonce, header.MixDigest = types.BlockNonce{}, common.Hash{}
		return block.WithSeal(header), nil
	}
	// If we're running a shared PoW, delegate sealing to it
	if canhash.shared != nil {
		return canhash.shared.Seal(chain, block, stop)
	}
	// Create a runner and the multiple search threads it directs
	abort := make(chan struct{})
	found := make(chan *types.Block)

	canhash.lock.Lock()
	threads := canhash.threads
	if canhash.rand == nil {
		seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
		if err != nil {
			canhash.lock.Unlock()
			return nil, err
		}
		canhash.rand = rand.New(rand.NewSource(seed.Int64()))
	}
	canhash.lock.Unlock()
	if threads == 0 {
		threads = runtime.NumCPU()
	}
	if threads < 0 {
		threads = 0 // Allows disabling local mining without extra logic around local/remote
	}
	var pend sync.WaitGroup
	for i := 0; i < threads; i++ {
		pend.Add(1)
		go func(id int, nonce uint64) {
			defer pend.Done()
			canhash.mine(block, id, nonce, abort, found)
		}(i, uint64(canhash.rand.Int63()))
	}
	// Wait until sealing is terminated or a nonce is found
	var result *types.Block
	select {
	case <-stop:
		// Outside abort, stop all miner threads
		close(abort)
	case result = <-found:
		// One of the threads found a block, abort all others
		close(abort)
	case <-canhash.update:
		// Thread count was changed on user request, restart
		close(abort)
		pend.Wait()
		return canhash.Seal(chain, block, stop)
	}
	// Wait for all miners to terminate and return the block
	pend.Wait()
	return result, nil
}

func (canhash *Canhash) mine(block *types.Block, id int, seed uint64, abort chan struct{}, found chan *types.Block) {
	// Extract some data from the header
	var (
		header  = block.Header()
		hash    = header.HashNoNonce().Bytes()
		target  = new(big.Int).Div(maxUint256, header.Difficulty)
		number  = header.Number.Uint64()
		dataset = canhash.dataset(number)
	)
	// Start generating random nonces until we abort or find a good one
	var (
		attempts = int64(0)
		nonce    = seed
	)
	logger := log4j.New("miner", id)
	logger.Trace("Started canhash search for new nonces", "seed", seed)
search:
	for {
		select {
		case <-abort:
			// Mining terminated, update stats and abort
			logger.Trace("Canhash nonce search aborted", "attempts", nonce-seed)
			canhash.hashrate.Mark(attempts)
			break search

		default:
			// We don't have to update hash rate on every nonce, so update after after 2^X nonces
			attempts++
			if (attempts % (1 << 15)) == 0 {
				canhash.hashrate.Mark(attempts)
				attempts = 0
			}
			// Compute the PoW value of this nonce
			digest, result := hashimotoFull(dataset.dataset, hash, nonce)
			if new(big.Int).SetBytes(result).Cmp(target) <= 0 {
				// Correct nonce found, create a new header with it
				header = types.CopyHeader(header)
				header.Nonce = types.EncodeNonce(nonce)
				header.MixDigest = common.BytesToHash(digest)

				// Seal and return a block (if still needed)
				select {
				case found <- block.WithSeal(header):
					logger.Trace("Canhash nonce found and reported", "attempts", nonce-seed, "nonce", nonce)
				case <-abort:
					logger.Trace("Canhash nonce found but discarded", "attempts", nonce-seed, "nonce", nonce)
				}
				break search
			}
			nonce++
		}
	}
	// Datasets are unmapped in a finalizer. Ensure that the dataset stays live
	// during sealing so it's not unmapped while being read.
	runtime.KeepAlive(dataset)
}
