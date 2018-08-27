package misc

import (
	"bytes"
	"errors"
	"math/big"

	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/params"
)

var (
	ErrBadProDAOExtra = errors.New("bad DAO pro-fork extra-data")

	ErrBadNoDAOExtra = errors.New("bad DAO no-fork extra-data")
)

func VerifyDAOHeaderExtraData(config *params.ChainConfig, header *types.Header) error {
	if config.DAOForkBlock == nil {
		return nil
	}
	limit := new(big.Int).Add(config.DAOForkBlock, params.DAOForkExtraRange)
	if header.Number.Cmp(config.DAOForkBlock) < 0 || header.Number.Cmp(limit) >= 0 {
		return nil
	}
	if config.DAOForkSupport {
		if !bytes.Equal(header.Extra, params.DAOForkBlockExtra) {
			return ErrBadProDAOExtra
		}
	} else {
		if bytes.Equal(header.Extra, params.DAOForkBlockExtra) {
			return ErrBadNoDAOExtra
		}
	}
	return nil
}

func ApplyDAOHardFork(statedb *state.StateDB) {
	if !statedb.Exist(params.DAORefundContract) {
		statedb.CreateAccount(params.DAORefundContract)
	}

	for _, addr := range params.DAODrainList() {
		statedb.AddBalance(params.DAORefundContract, statedb.GetBalance(addr))
		statedb.SetBalance(addr, new(big.Int))
	}
}
