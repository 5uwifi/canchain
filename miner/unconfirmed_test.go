package miner

import (
	"testing"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
)

type noopHeaderRetriever struct{}

func (r *noopHeaderRetriever) GetHeaderByNumber(number uint64) *types.Header {
	return nil
}

func TestUnconfirmedInsertBounds(t *testing.T) {
	limit := uint(10)

	pool := newUnconfirmedBlocks(new(noopHeaderRetriever), limit)
	for depth := uint64(0); depth < 2*uint64(limit); depth++ {
		for i := 0; i < int(depth); i++ {
			pool.Insert(depth, common.Hash([32]byte{byte(depth), byte(i)}))
		}
		pool.blocks.Do(func(block interface{}) {
			if block := block.(*unconfirmedBlock); block.index+uint64(limit) <= depth {
				t.Errorf("depth %d: block %x not dropped", depth, block.hash)
			}
		})
	}
}

func TestUnconfirmedShifts(t *testing.T) {
	limit, start := uint(10), uint64(25)

	pool := newUnconfirmedBlocks(new(noopHeaderRetriever), limit)
	for depth := start; depth < start+uint64(limit); depth++ {
		pool.Insert(depth, common.Hash([32]byte{byte(depth)}))
	}
	pool.Shift(start + uint64(limit) - 1)
	if n := pool.blocks.Len(); n != int(limit) {
		t.Errorf("unconfirmed count mismatch: have %d, want %d", n, limit)
	}
	pool.Shift(start + uint64(limit) - 1 + uint64(limit/2))
	if n := pool.blocks.Len(); n != int(limit)/2 {
		t.Errorf("unconfirmed count mismatch: have %d, want %d", n, limit/2)
	}
	pool.Shift(start + 2*uint64(limit))
	if n := pool.blocks.Len(); n != 0 {
		t.Errorf("unconfirmed count mismatch: have %d, want %d", n, 0)
	}
	pool.Shift(start + 3*uint64(limit))
	if n := pool.blocks.Len(); n != 0 {
		t.Errorf("unconfirmed count mismatch: have %d, want %d", n, 0)
	}
}
