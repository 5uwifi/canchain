
package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/common/math"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/state"
	"github.com/5uwifi/canchain/kernel/vm"
	"github.com/5uwifi/canchain/basis/crypto"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/params"
)

type VMTest struct {
	json vmJSON
}

func (t *VMTest) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &t.json)
}

type vmJSON struct {
	Env           stEnv                 `json:"env"`
	Exec          vmExec                `json:"exec"`
	Logs          common.UnprefixedHash `json:"logs"`
	GasRemaining  *math.HexOrDecimal64  `json:"gas"`
	Out           hexutil.Bytes         `json:"out"`
	Pre           kernel.GenesisAlloc   `json:"pre"`
	Post          kernel.GenesisAlloc   `json:"post"`
	PostStateRoot common.Hash           `json:"postStateRoot"`
}


type vmExec struct {
	Address  common.Address `json:"address"  gencodec:"required"`
	Caller   common.Address `json:"caller"   gencodec:"required"`
	Origin   common.Address `json:"origin"   gencodec:"required"`
	Code     []byte         `json:"code"     gencodec:"required"`
	Data     []byte         `json:"data"     gencodec:"required"`
	Value    *big.Int       `json:"value"    gencodec:"required"`
	GasLimit uint64         `json:"gas"      gencodec:"required"`
	GasPrice *big.Int       `json:"gasPrice" gencodec:"required"`
}

type vmExecMarshaling struct {
	Address  common.UnprefixedAddress
	Caller   common.UnprefixedAddress
	Origin   common.UnprefixedAddress
	Code     hexutil.Bytes
	Data     hexutil.Bytes
	Value    *math.HexOrDecimal256
	GasLimit math.HexOrDecimal64
	GasPrice *math.HexOrDecimal256
}

func (t *VMTest) Run(vmconfig vm.Config) error {
	statedb := MakePreState(candb.NewMemDatabase(), t.json.Pre)
	ret, gasRemaining, err := t.exec(statedb, vmconfig)

	if t.json.GasRemaining == nil {
		if err == nil {
			return fmt.Errorf("gas unspecified (indicating an error), but VM returned no error")
		}
		if gasRemaining > 0 {
			return fmt.Errorf("gas unspecified (indicating an error), but VM returned gas remaining > 0")
		}
		return nil
	}
	// Test declares gas, expecting outputs to match.
	if !bytes.Equal(ret, t.json.Out) {
		return fmt.Errorf("return data mismatch: got %x, want %x", ret, t.json.Out)
	}
	if gasRemaining != uint64(*t.json.GasRemaining) {
		return fmt.Errorf("remaining gas %v, want %v", gasRemaining, *t.json.GasRemaining)
	}
	for addr, account := range t.json.Post {
		for k, wantV := range account.Storage {
			if haveV := statedb.GetState(addr, k); haveV != wantV {
				return fmt.Errorf("wrong storage value at %x:\n  got  %x\n  want %x", k, haveV, wantV)
			}
		}
	}
	// if root := statedb.IntermediateRoot(false); root != t.json.PostStateRoot {
	// 	return fmt.Errorf("post state root mismatch, got %x, want %x", root, t.json.PostStateRoot)
	// }
	if logs := rlpHash(statedb.Logs()); logs != common.Hash(t.json.Logs) {
		return fmt.Errorf("post state logs hash mismatch: got %x, want %x", logs, t.json.Logs)
	}
	return nil
}

func (t *VMTest) exec(statedb *state.StateDB, vmconfig vm.Config) ([]byte, uint64, error) {
	evm := t.newEVM(statedb, vmconfig)
	e := t.json.Exec
	return evm.Call(vm.AccountRef(e.Caller), e.Address, e.Data, e.GasLimit, e.Value)
}

func (t *VMTest) newEVM(statedb *state.StateDB, vmconfig vm.Config) *vm.EVM {
	initialCall := true
	canTransfer := func(db vm.StateDB, address common.Address, amount *big.Int) bool {
		if initialCall {
			initialCall = false
			return true
		}
		return kernel.CanTransfer(db, address, amount)
	}
	transfer := func(db vm.StateDB, sender, recipient common.Address, amount *big.Int) {}
	context := vm.Context{
		CanTransfer: canTransfer,
		Transfer:    transfer,
		GetHash:     vmTestBlockHash,
		Origin:      t.json.Exec.Origin,
		Coinbase:    t.json.Env.Coinbase,
		BlockNumber: new(big.Int).SetUint64(t.json.Env.Number),
		Time:        new(big.Int).SetUint64(t.json.Env.Timestamp),
		GasLimit:    t.json.Env.GasLimit,
		Difficulty:  t.json.Env.Difficulty,
		GasPrice:    t.json.Exec.GasPrice,
	}
	vmconfig.NoRecursion = true
	return vm.NewEVM(context, statedb, params.MainnetChainConfig, vmconfig)
}

func vmTestBlockHash(n uint64) common.Hash {
	return common.BytesToHash(crypto.Keccak256([]byte(big.NewInt(int64(n)).String())))
}
