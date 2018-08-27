package core

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/5uwifi/canchain/common"
)


func (vs *ValidationMessages) crit(msg string) {
	vs.Messages = append(vs.Messages, ValidationInfo{"CRITICAL", msg})
}
func (vs *ValidationMessages) warn(msg string) {
	vs.Messages = append(vs.Messages, ValidationInfo{"WARNING", msg})
}
func (vs *ValidationMessages) info(msg string) {
	vs.Messages = append(vs.Messages, ValidationInfo{"Info", msg})
}

type Validator struct {
	db *AbiDb
}

func NewValidator(db *AbiDb) *Validator {
	return &Validator{db}
}
func testSelector(selector string, data []byte) (*decodedCallData, error) {
	if selector == "" {
		return nil, fmt.Errorf("selector not found")
	}
	abiData, err := MethodSelectorToAbi(selector)
	if err != nil {
		return nil, err
	}
	info, err := parseCallData(data, string(abiData))
	if err != nil {
		return nil, err
	}
	return info, nil

}

func (v *Validator) validateCallData(msgs *ValidationMessages, data []byte, methodSelector *string) {
	if len(data) == 0 {
		return
	}
	if len(data) < 4 {
		msgs.warn("Tx contains data which is not valid ABI")
		return
	}
	var (
		info *decodedCallData
		err  error
	)
	if methodSelector != nil {
		info, err = testSelector(*methodSelector, data)
		if err != nil {
			msgs.warn(fmt.Sprintf("Tx contains data, but provided ABI signature could not be matched: %v", err))
		} else {
			msgs.info(info.String())
			v.db.AddSignature(*methodSelector, data[:4])
		}
		return
	}
	selector, err := v.db.LookupMethodSelector(data[:4])
	if err != nil {
		msgs.warn(fmt.Sprintf("Tx contains data, but the ABI signature could not be found: %v", err))
		return
	}
	info, err = testSelector(selector, data)
	if err != nil {
		msgs.warn(fmt.Sprintf("Tx contains data, but provided ABI signature could not be matched: %v", err))
	} else {
		msgs.info(info.String())
	}
}

func (v *Validator) validate(msgs *ValidationMessages, txargs *SendTxArgs, methodSelector *string) error {
	if txargs.Data != nil && txargs.Input != nil && !bytes.Equal(*txargs.Data, *txargs.Input) {
		return errors.New(`Ambiguous request: both "data" and "input" are set and are not identical`)
	}
	var (
		data []byte
	)
	if txargs.Input != nil {
		txargs.Data = txargs.Input
		txargs.Input = nil
	}
	if txargs.Data != nil {
		data = *txargs.Data
	}

	if txargs.To == nil {
		if len(data) == 0 {
			if txargs.Value.ToInt().Cmp(big.NewInt(0)) > 0 {
				return errors.New("Tx will create contract with value but empty code!")
			}
			msgs.crit("Tx will create contract with empty code!")
		} else if len(data) < 40 {
			msgs.warn(fmt.Sprintf("Tx will will create contract, but payload is suspiciously small (%d b)", len(data)))
		}
		if methodSelector != nil {
			msgs.warn("Tx will create contract, but method selector supplied; indicating intent to call a method.")
		}

	} else {
		if !txargs.To.ValidChecksum() {
			msgs.warn("Invalid checksum on to-address")
		}
		if bytes.Equal(txargs.To.Address().Bytes(), common.Address{}.Bytes()) {
			msgs.crit("Tx destination is the zero address!")
		}
		v.validateCallData(msgs, data, methodSelector)
	}
	return nil
}

func (v *Validator) ValidateTransaction(txArgs *SendTxArgs, methodSelector *string) (*ValidationMessages, error) {
	msgs := &ValidationMessages{}
	return msgs, v.validate(msgs, txArgs, methodSelector)
}
