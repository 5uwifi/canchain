package consensus

import (
	"math/big"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/rpc"
)

type ChainReader interface {
	Config() *params.ChainConfig

	CurrentHeader() *types.Header

	GetHeader(hash common.Hash, number uint64) *types.Header

	GetHeaderByNumber(number uint64) *types.Header

	GetHeaderByHash(hash common.Hash) *types.Header

	GetBlock(hash common.Hash, number uint64) *types.Block
}

type Engine interface {
	Author(header *types.Header) (common.Address, error)

	VerifyHeader(chain ChainReader, header *types.Header, seal bool) error

	VerifyHeaders(chain ChainReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error)

	VerifyUncles(chain ChainReader, block *types.Block) error

	VerifySeal(chain ChainReader, header *types.Header) error

	Prepare(chain ChainReader, header *types.Header) error

	Finalize(chain ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction,
		uncles []*types.Header, receipts []*types.Receipt) (*types.Block, error)

	Seal(chain ChainReader, block *types.Block, stop <-chan struct{}) (*types.Block, error)

	SealHash(header *types.Header) common.Hash

	CalcDifficulty(chain ChainReader, time uint64, parent *types.Header) *big.Int

	APIs(chain ChainReader) []rpc.API

	Close() error
}

type PoW interface {
	Engine

	Hashrate() float64
}
