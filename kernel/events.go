
package kernel

import (
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
)

type NewTxsEvent struct{ Txs []*types.Transaction }

type PendingLogsEvent struct {
	Logs []*types.Log
}

type PendingStateEvent struct{}

type NewMinedBlockEvent struct{ Block *types.Block }

type RemovedLogsEvent struct{ Logs []*types.Log }

type ChainEvent struct {
	Block *types.Block
	Hash  common.Hash
	Logs  []*types.Log
}

type ChainSideEvent struct {
	Block *types.Block
}

type ChainHeadEvent struct{ Block *types.Block }
