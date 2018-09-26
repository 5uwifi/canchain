package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"math/big"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/kernel/types"
)

type Accounts []Account

func (as Accounts) String() string {
	var output []string
	for _, a := range as {
		output = append(output, a.String())
	}
	return strings.Join(output, "\n")
}

type Account struct {
	Typ     string         `json:"type"`
	URL     accounts.URL   `json:"url"`
	Address common.Address `json:"address"`
}

func (a Account) String() string {
	s, err := json.Marshal(a)
	if err == nil {
		return string(s)
	}
	return err.Error()
}

type ValidationInfo struct {
	Typ     string `json:"type"`
	Message string `json:"message"`
}
type ValidationMessages struct {
	Messages []ValidationInfo
}

const (
	WARN = "WARNING"
	CRIT = "CRITICAL"
	INFO = "Info"
)

func (vs *ValidationMessages) crit(msg string) {
	vs.Messages = append(vs.Messages, ValidationInfo{CRIT, msg})
}
func (vs *ValidationMessages) warn(msg string) {
	vs.Messages = append(vs.Messages, ValidationInfo{WARN, msg})
}
func (vs *ValidationMessages) info(msg string) {
	vs.Messages = append(vs.Messages, ValidationInfo{INFO, msg})
}

func (v *ValidationMessages) getWarnings() error {
	var messages []string
	for _, msg := range v.Messages {
		if msg.Typ == WARN || msg.Typ == CRIT {
			messages = append(messages, msg.Message)
		}
	}
	if len(messages) > 0 {
		return fmt.Errorf("Validation failed: %s", strings.Join(messages, ","))
	}
	return nil
}

type SendTxArgs struct {
	From     common.MixedcaseAddress  `json:"from"`
	To       *common.MixedcaseAddress `json:"to"`
	Gas      hexutil.Uint64           `json:"gas"`
	GasPrice hexutil.Big              `json:"gasPrice"`
	Value    hexutil.Big              `json:"value"`
	Nonce    hexutil.Uint64           `json:"nonce"`
	Data     *hexutil.Bytes           `json:"data"`
	Input    *hexutil.Bytes           `json:"input"`
}

func (args SendTxArgs) String() string {
	s, err := json.Marshal(args)
	if err == nil {
		return string(s)
	}
	return err.Error()
}

func (args *SendTxArgs) toTransaction() *types.Transaction {
	var input []byte
	if args.Data != nil {
		input = *args.Data
	} else if args.Input != nil {
		input = *args.Input
	}
	if args.To == nil {
		return types.NewContractCreation(uint64(args.Nonce), (*big.Int)(&args.Value), uint64(args.Gas), (*big.Int)(&args.GasPrice), input)
	}
	return types.NewTransaction(uint64(args.Nonce), args.To.Address(), (*big.Int)(&args.Value), (uint64)(args.Gas), (*big.Int)(&args.GasPrice), input)
}
