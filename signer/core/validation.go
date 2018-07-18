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
	// Check the provided one
	if methodSelector != nil {
		info, err = testSelector(*methodSelector, data)
		if err != nil {
			msgs.warn(fmt.Sprintf("Tx contains data, but provided ABI signature could not be matched: %v", err))
		} else {
			msgs.info(info.String())
			//Successfull match. add to db if not there already (ignore errors there)
			v.db.AddSignature(*methodSelector, data[:4])
		}
		return
	}
	// Check the db
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
	// Prevent accidental erroneous usage of both 'input' and 'data'
	if txargs.Data != nil && txargs.Input != nil && !bytes.Equal(*txargs.Data, *txargs.Input) {
		// This is a showstopper
		return errors.New(`Ambiguous request: both "data" and "input" are set and are not identical`)
	}
	var (
		data []byte
	)
	// Place data on 'data', and nil 'input'
	if txargs.Input != nil {
		txargs.Data = txargs.Input
		txargs.Input = nil
	}
	if txargs.Data != nil {
		data = *txargs.Data
	}

	if txargs.To == nil {
		//Contract creation should contain sufficient data to deploy a contract
		// A typical error is omitting sender due to some quirk in the javascript call
		// e.g. https://github.com/5uwifi/canchain/issues/16106
		if len(data) == 0 {
			if txargs.Value.ToInt().Cmp(big.NewInt(0)) > 0 {
				// Sending ether into black hole
				return errors.New("Tx will create contract with value but empty code!")
			}
			// No value submitted at least
			msgs.crit("Tx will create contract with empty code!")
		} else if len(data) < 40 { //Arbitrary limit
			msgs.warn(fmt.Sprintf("Tx will will create contract, but payload is suspiciously small (%d b)", len(data)))
		}
		// methodSelector should be nil for contract creation
		if methodSelector != nil {
			msgs.warn("Tx will create contract, but method selector supplied; indicating intent to call a method.")
		}

	} else {
		if !txargs.To.ValidChecksum() {
			msgs.warn("Invalid checksum on to-address")
		}
		// Normal transaction
		if bytes.Equal(txargs.To.Address().Bytes(), common.Address{}.Bytes()) {
			// Sending to 0
			msgs.crit("Tx destination is the zero address!")
		}
		// Validate calldata
		v.validateCallData(msgs, data, methodSelector)
	}
	return nil
}

func (v *Validator) ValidateTransaction(txArgs *SendTxArgs, methodSelector *string) (*ValidationMessages, error) {
	msgs := &ValidationMessages{}
	return msgs, v.validate(msgs, txArgs, methodSelector)
}
