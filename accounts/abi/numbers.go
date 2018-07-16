package abi

import (
	"math/big"
	"reflect"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/math"
)

var (
	bigT      = reflect.TypeOf(&big.Int{})
	derefbigT = reflect.TypeOf(big.Int{})
	uint8T    = reflect.TypeOf(uint8(0))
	uint16T   = reflect.TypeOf(uint16(0))
	uint32T   = reflect.TypeOf(uint32(0))
	uint64T   = reflect.TypeOf(uint64(0))
	int8T     = reflect.TypeOf(int8(0))
	int16T    = reflect.TypeOf(int16(0))
	int32T    = reflect.TypeOf(int32(0))
	int64T    = reflect.TypeOf(int64(0))
	addressT  = reflect.TypeOf(common.Address{})
)

func U256(n *big.Int) []byte {
	return math.PaddedBigBytes(math.U256(n), 32)
}
