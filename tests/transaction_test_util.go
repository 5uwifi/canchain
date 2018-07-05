//
// (at your option) any later version.
//
//

package tests

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/common/math"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/params"
	"github.com/5uwifi/canchain/basis/rlp"
)

type TransactionTest struct {
	json ttJSON
}

type ttJSON struct {
	BlockNumber math.HexOrDecimal64 `json:"blockNumber"`
	RLP         hexutil.Bytes       `json:"rlp"`
	Sender      hexutil.Bytes       `json:"sender"`
	Transaction *ttTransaction      `json:"transaction"`
}


type ttTransaction struct {
	Data     []byte         `gencodec:"required"`
	GasLimit uint64         `gencodec:"required"`
	GasPrice *big.Int       `gencodec:"required"`
	Nonce    uint64         `gencodec:"required"`
	Value    *big.Int       `gencodec:"required"`
	R        *big.Int       `gencodec:"required"`
	S        *big.Int       `gencodec:"required"`
	V        *big.Int       `gencodec:"required"`
	To       common.Address `gencodec:"required"`
}

type ttTransactionMarshaling struct {
	Data     hexutil.Bytes
	GasLimit math.HexOrDecimal64
	GasPrice *math.HexOrDecimal256
	Nonce    math.HexOrDecimal64
	Value    *math.HexOrDecimal256
	R        *math.HexOrDecimal256
	S        *math.HexOrDecimal256
	V        *math.HexOrDecimal256
}

func (tt *TransactionTest) Run(config *params.ChainConfig) error {
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(tt.json.RLP, tx); err != nil {
		if tt.json.Transaction == nil {
			return nil
		}
		return fmt.Errorf("RLP decoding failed: %v", err)
	}
	// Check sender derivation.
	signer := types.MakeSigner(config, new(big.Int).SetUint64(uint64(tt.json.BlockNumber)))
	sender, err := types.Sender(signer, tx)
	if err != nil {
		return err
	}
	if sender != common.BytesToAddress(tt.json.Sender) {
		return fmt.Errorf("Sender mismatch: got %x, want %x", sender, tt.json.Sender)
	}
	// Check decoded fields.
	err = tt.json.Transaction.verify(signer, tx)
	if tt.json.Sender == nil && err == nil {
		return errors.New("field validations succeeded but should fail")
	}
	if tt.json.Sender != nil && err != nil {
		return fmt.Errorf("field validations failed after RLP decoding: %s", err)
	}
	return nil
}

func (tt *ttTransaction) verify(signer types.Signer, tx *types.Transaction) error {
	if !bytes.Equal(tx.Data(), tt.Data) {
		return fmt.Errorf("Tx input data mismatch: got %x want %x", tx.Data(), tt.Data)
	}
	if tx.Gas() != tt.GasLimit {
		return fmt.Errorf("GasLimit mismatch: got %d, want %d", tx.Gas(), tt.GasLimit)
	}
	if tx.GasPrice().Cmp(tt.GasPrice) != 0 {
		return fmt.Errorf("GasPrice mismatch: got %v, want %v", tx.GasPrice(), tt.GasPrice)
	}
	if tx.Nonce() != tt.Nonce {
		return fmt.Errorf("Nonce mismatch: got %v, want %v", tx.Nonce(), tt.Nonce)
	}
	v, r, s := tx.RawSignatureValues()
	if r.Cmp(tt.R) != 0 {
		return fmt.Errorf("R mismatch: got %v, want %v", r, tt.R)
	}
	if s.Cmp(tt.S) != 0 {
		return fmt.Errorf("S mismatch: got %v, want %v", s, tt.S)
	}
	if v.Cmp(tt.V) != 0 {
		return fmt.Errorf("V mismatch: got %v, want %v", v, tt.V)
	}
	if tx.To() == nil {
		if tt.To != (common.Address{}) {
			return fmt.Errorf("To mismatch when recipient is nil (contract creation): %x", tt.To)
		}
	} else if *tx.To() != tt.To {
		return fmt.Errorf("To mismatch: got %x, want %x", *tx.To(), tt.To)
	}
	if tx.Value().Cmp(tt.Value) != 0 {
		return fmt.Errorf("Value mismatch: got %x, want %x", tx.Value(), tt.Value)
	}
	return nil
}
