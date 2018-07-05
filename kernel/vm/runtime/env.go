//
// (at your option) any later version.
//
//

package runtime

import (
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/vm"
)

func NewEnv(cfg *Config) *vm.EVM {
	context := vm.Context{
		CanTransfer: kernel.CanTransfer,
		Transfer:    kernel.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },

		Origin:      cfg.Origin,
		Coinbase:    cfg.Coinbase,
		BlockNumber: cfg.BlockNumber,
		Time:        cfg.Time,
		Difficulty:  cfg.Difficulty,
		GasLimit:    cfg.GasLimit,
		GasPrice:    cfg.GasPrice,
	}

	return vm.NewEVM(context, cfg.State, cfg.ChainConfig, cfg.EVMConfig)
}
