package vm

import (
	"fmt"
	"hash"
	"sync/atomic"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/math"
	"github.com/5uwifi/canchain/params"
)

type Config struct {
	Debug bool
	Tracer Tracer
	NoRecursion bool
	EnablePreimageRecording bool
	JumpTable [256]operation

	EWASMInterpreter string
	EVMInterpreter string
}

type Interpreter interface {
	Run(contract *Contract, input []byte, static bool) ([]byte, error)
	CanRun([]byte) bool
}

type keccakState interface {
	hash.Hash
	Read([]byte) (int, error)
}

type EVMInterpreter struct {
	evm      *EVM
	cfg      Config
	gasTable params.GasTable

	intPool *intPool

	hasher    keccakState
	hasherBuf common.Hash

	readOnly   bool
	returnData []byte
}

func NewEVMInterpreter(evm *EVM, cfg Config) *EVMInterpreter {
	if !cfg.JumpTable[STOP].valid {
		switch {
		case evm.ChainConfig().IsConstantinople(evm.BlockNumber):
			cfg.JumpTable = constantinopleInstructionSet
		case evm.ChainConfig().IsByzantium(evm.BlockNumber):
			cfg.JumpTable = byzantiumInstructionSet
		case evm.ChainConfig().IsHomestead(evm.BlockNumber):
			cfg.JumpTable = homesteadInstructionSet
		default:
			cfg.JumpTable = frontierInstructionSet
		}
	}

	return &EVMInterpreter{
		evm:      evm,
		cfg:      cfg,
		gasTable: evm.ChainConfig().GasTable(evm.BlockNumber),
	}
}

func (in *EVMInterpreter) enforceRestrictions(op OpCode, operation operation, stack *Stack) error {
	if in.evm.chainRules.IsByzantium {
		if in.readOnly {
			if operation.writes || (op == CALL && stack.Back(2).BitLen() > 0) {
				return errWriteProtection
			}
		}
	}
	return nil
}

func (in *EVMInterpreter) Run(contract *Contract, input []byte, readOnly bool) (ret []byte, err error) {
	if in.intPool == nil {
		in.intPool = poolOfIntPools.get()
		defer func() {
			poolOfIntPools.put(in.intPool)
			in.intPool = nil
		}()
	}

	in.evm.depth++
	defer func() { in.evm.depth-- }()

	if readOnly && !in.readOnly {
		in.readOnly = true
		defer func() { in.readOnly = false }()
	}

	in.returnData = nil

	if len(contract.Code) == 0 {
		return nil, nil
	}

	var (
		op    OpCode
		mem   = NewMemory()
		stack = newstack()
		pc   = uint64(0)
		cost uint64
		pcCopy  uint64
		gasCopy uint64
		logged  bool
	)
	contract.Input = input

	defer func() { in.intPool.put(stack.data...) }()

	if in.cfg.Debug {
		defer func() {
			if err != nil {
				if !logged {
					in.cfg.Tracer.CaptureState(in.evm, pcCopy, op, gasCopy, cost, mem, stack, contract, in.evm.depth, err)
				} else {
					in.cfg.Tracer.CaptureFault(in.evm, pcCopy, op, gasCopy, cost, mem, stack, contract, in.evm.depth, err)
				}
			}
		}()
	}
	for atomic.LoadInt32(&in.evm.abort) == 0 {
		if in.cfg.Debug {
			logged, pcCopy, gasCopy = false, pc, contract.Gas
		}

		op = contract.GetOp(pc)
		operation := in.cfg.JumpTable[op]
		if !operation.valid {
			return nil, fmt.Errorf("invalid opcode 0x%x", int(op))
		}
		if err := operation.validateStack(stack); err != nil {
			return nil, err
		}
		if err := in.enforceRestrictions(op, operation, stack); err != nil {
			return nil, err
		}

		var memorySize uint64
		if operation.memorySize != nil {
			memSize, overflow := bigUint64(operation.memorySize(stack))
			if overflow {
				return nil, errGasUintOverflow
			}
			if memorySize, overflow = math.SafeMul(toWordSize(memSize), 32); overflow {
				return nil, errGasUintOverflow
			}
		}
		cost, err = operation.gasCost(in.gasTable, in.evm, contract, stack, mem, memorySize)
		if err != nil || !contract.UseGas(cost) {
			return nil, ErrOutOfGas
		}
		if memorySize > 0 {
			mem.Resize(memorySize)
		}

		if in.cfg.Debug {
			in.cfg.Tracer.CaptureState(in.evm, pc, op, gasCopy, cost, mem, stack, contract, in.evm.depth, err)
			logged = true
		}

		res, err := operation.execute(&pc, in, contract, mem, stack)
		if verifyPool {
			verifyIntegerPool(in.intPool)
		}
		if operation.returns {
			in.returnData = res
		}

		switch {
		case err != nil:
			return nil, err
		case operation.reverts:
			return res, errExecutionReverted
		case operation.halts:
			return res, nil
		case !operation.jumps:
			pc++
		}
	}
	return nil, nil
}

func (in *EVMInterpreter) CanRun(code []byte) bool {
	return true
}
