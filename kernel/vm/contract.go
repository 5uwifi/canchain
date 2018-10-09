package vm

import (
	"math/big"

	"github.com/5uwifi/canchain/common"
)

type ContractRef interface {
	Address() common.Address
}

type AccountRef common.Address

func (ar AccountRef) Address() common.Address { return (common.Address)(ar) }

type Contract struct {
	CallerAddress common.Address
	caller        ContractRef
	self          ContractRef

	jumpdests map[common.Hash]bitvec
	analysis  bitvec

	Code     []byte
	CodeHash common.Hash
	CodeAddr *common.Address
	Input    []byte

	Gas   uint64
	value *big.Int
}

func NewContract(caller ContractRef, object ContractRef, value *big.Int, gas uint64) *Contract {
	c := &Contract{CallerAddress: caller.Address(), caller: caller, self: object}

	if parent, ok := caller.(*Contract); ok {
		c.jumpdests = parent.jumpdests
	} else {
		c.jumpdests = make(map[common.Hash]bitvec)
	}

	c.Gas = gas
	c.value = value

	return c
}

func (c *Contract) validJumpdest(dest *big.Int) bool {
	udest := dest.Uint64()
	if dest.BitLen() >= 63 || udest >= uint64(len(c.Code)) {
		return false
	}
	if OpCode(c.Code[udest]) != JUMPDEST {
		return false
	}
	if c.CodeHash != (common.Hash{}) {
		analysis, exist := c.jumpdests[c.CodeHash]
		if !exist {
			analysis = codeBitmap(c.Code)
			c.jumpdests[c.CodeHash] = analysis
		}
		return analysis.codeSegment(udest)
	}
	if c.analysis == nil {
		c.analysis = codeBitmap(c.Code)
	}
	return c.analysis.codeSegment(udest)
}

func (c *Contract) AsDelegate() *Contract {
	parent := c.caller.(*Contract)
	c.CallerAddress = parent.CallerAddress
	c.value = parent.value

	return c
}

func (c *Contract) GetOp(n uint64) OpCode {
	return OpCode(c.GetByte(n))
}

func (c *Contract) GetByte(n uint64) byte {
	if n < uint64(len(c.Code)) {
		return c.Code[n]
	}

	return 0
}

func (c *Contract) Caller() common.Address {
	return c.CallerAddress
}

func (c *Contract) UseGas(gas uint64) (ok bool) {
	if c.Gas < gas {
		return false
	}
	c.Gas -= gas
	return true
}

func (c *Contract) Address() common.Address {
	return c.self.Address()
}

func (c *Contract) Value() *big.Int {
	return c.value
}

func (c *Contract) SetCallCode(addr *common.Address, hash common.Hash, code []byte) {
	c.Code = code
	c.CodeHash = hash
	c.CodeAddr = addr
}

func (c *Contract) SetCodeOptionalHash(addr *common.Address, codeAndHash *codeAndHash) {
	c.Code = codeAndHash.code
	c.CodeHash = codeAndHash.hash
	c.CodeAddr = addr
}
