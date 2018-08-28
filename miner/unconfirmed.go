package miner

import (
	"container/ring"
	"sync"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/log4j"
)

type chainRetriever interface {
	GetHeaderByNumber(number uint64) *types.Header

	GetBlockByNumber(number uint64) *types.Block
}

type unconfirmedBlock struct {
	index uint64
	hash  common.Hash
}

type unconfirmedBlocks struct {
	chain  chainRetriever
	depth  uint
	blocks *ring.Ring
	lock   sync.RWMutex
}

func newUnconfirmedBlocks(chain chainRetriever, depth uint) *unconfirmedBlocks {
	return &unconfirmedBlocks{
		chain: chain,
		depth: depth,
	}
}

func (set *unconfirmedBlocks) Insert(index uint64, hash common.Hash) {
	set.Shift(index)

	item := ring.New(1)
	item.Value = &unconfirmedBlock{
		index: index,
		hash:  hash,
	}
	set.lock.Lock()
	defer set.lock.Unlock()

	if set.blocks == nil {
		set.blocks = item
	} else {
		set.blocks.Move(-1).Link(item)
	}
	log4j.Info("🔨 mined potential block", "number", index, "hash", hash)
}

func (set *unconfirmedBlocks) Shift(height uint64) {
	set.lock.Lock()
	defer set.lock.Unlock()

	for set.blocks != nil {
		next := set.blocks.Value.(*unconfirmedBlock)
		if next.index+uint64(set.depth) > height {
			break
		}
		header := set.chain.GetHeaderByNumber(next.index)
		switch {
		case header == nil:
			log4j.Warn("Failed to retrieve header of mined block", "number", next.index, "hash", next.hash)
		case header.Hash() == next.hash:
			log4j.Info("🔗 block reached canonical chain", "number", next.index, "hash", next.hash)
		default:
			included := false
			for number := next.index; !included && number < next.index+uint64(set.depth) && number <= height; number++ {
				if block := set.chain.GetBlockByNumber(number); block != nil {
					for _, uncle := range block.Uncles() {
						if uncle.Hash() == next.hash {
							included = true
							break
						}
					}
				}
			}
			if included {
				log4j.Info("⑂ block became an uncle", "number", next.index, "hash", next.hash)
			} else {
				log4j.Info("😱 block lost", "number", next.index, "hash", next.hash)
			}
		}
		if set.blocks.Value == set.blocks.Next().Value {
			set.blocks = nil
		} else {
			set.blocks = set.blocks.Move(-1)
			set.blocks.Unlink(1)
			set.blocks = set.blocks.Move(1)
		}
	}
}
