//
// (at your option) any later version.
//
//

package bind

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/accounts/abi"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/basis/crypto"
	"github.com/5uwifi/canchain/basis/event"
)

type SignerFn func(types.Signer, common.Address, *types.Transaction) (*types.Transaction, error)

type CallOpts struct {
	Pending bool           // Whether to operate on the pending state or the last known one
	From    common.Address // Optional the sender address, otherwise the first account is used

	Context context.Context // Network context to support cancellation and timeouts (nil = no timeout)
}

type TransactOpts struct {
	From   common.Address // Ethereum account to send the transaction from
	Nonce  *big.Int       // Nonce to use for the transaction execution (nil = use pending state)
	Signer SignerFn       // Method to use for signing the transaction (mandatory)

	Value    *big.Int // Funds to transfer along along the transaction (nil = 0 = no funds)
	GasPrice *big.Int // Gas price to use for the transaction execution (nil = gas price oracle)
	GasLimit uint64   // Gas limit to set for the transaction execution (0 = estimate)

	Context context.Context // Network context to support cancellation and timeouts (nil = no timeout)
}

type FilterOpts struct {
	Start uint64  // Start of the queried range
	End   *uint64 // End of the range (nil = latest)

	Context context.Context // Network context to support cancellation and timeouts (nil = no timeout)
}

type WatchOpts struct {
	Start   *uint64         // Start of the queried range (nil = latest)
	Context context.Context // Network context to support cancellation and timeouts (nil = no timeout)
}

type BoundContract struct {
	address    common.Address     // Deployment address of the contract on the Ethereum blockchain
	abi        abi.ABI            // Reflect based ABI to access the correct Ethereum methods
	caller     ContractCaller     // Read interface to interact with the blockchain
	transactor ContractTransactor // Write interface to interact with the blockchain
	filterer   ContractFilterer   // Event filtering to interact with the blockchain
}

func NewBoundContract(address common.Address, abi abi.ABI, caller ContractCaller, transactor ContractTransactor, filterer ContractFilterer) *BoundContract {
	return &BoundContract{
		address:    address,
		abi:        abi,
		caller:     caller,
		transactor: transactor,
		filterer:   filterer,
	}
}

func DeployContract(opts *TransactOpts, abi abi.ABI, bytecode []byte, backend ContractBackend, params ...interface{}) (common.Address, *types.Transaction, *BoundContract, error) {
	// Otherwise try to deploy the contract
	c := NewBoundContract(common.Address{}, abi, backend, backend, backend)

	input, err := c.abi.Pack("", params...)
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	tx, err := c.transact(opts, nil, append(bytecode, input...))
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	c.address = crypto.CreateAddress(opts.From, tx.Nonce())
	return c.address, tx, c, nil
}

func (c *BoundContract) Call(opts *CallOpts, result interface{}, method string, params ...interface{}) error {
	// Don't crash on a lazy user
	if opts == nil {
		opts = new(CallOpts)
	}
	// Pack the input, call and unpack the results
	input, err := c.abi.Pack(method, params...)
	if err != nil {
		return err
	}
	var (
		msg    = canchain.CallMsg{From: opts.From, To: &c.address, Data: input}
		ctx    = ensureContext(opts.Context)
		code   []byte
		output []byte
	)
	if opts.Pending {
		pb, ok := c.caller.(PendingContractCaller)
		if !ok {
			return ErrNoPendingState
		}
		output, err = pb.PendingCallContract(ctx, msg)
		if err == nil && len(output) == 0 {
			// Make sure we have a contract to operate on, and bail out otherwise.
			if code, err = pb.PendingCodeAt(ctx, c.address); err != nil {
				return err
			} else if len(code) == 0 {
				return ErrNoCode
			}
		}
	} else {
		output, err = c.caller.CallContract(ctx, msg, nil)
		if err == nil && len(output) == 0 {
			// Make sure we have a contract to operate on, and bail out otherwise.
			if code, err = c.caller.CodeAt(ctx, c.address, nil); err != nil {
				return err
			} else if len(code) == 0 {
				return ErrNoCode
			}
		}
	}
	if err != nil {
		return err
	}
	return c.abi.Unpack(result, method, output)
}

func (c *BoundContract) Transact(opts *TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	// Otherwise pack up the parameters and invoke the contract
	input, err := c.abi.Pack(method, params...)
	if err != nil {
		return nil, err
	}
	return c.transact(opts, &c.address, input)
}

func (c *BoundContract) Transfer(opts *TransactOpts) (*types.Transaction, error) {
	return c.transact(opts, &c.address, nil)
}

func (c *BoundContract) transact(opts *TransactOpts, contract *common.Address, input []byte) (*types.Transaction, error) {
	var err error

	// Ensure a valid value field and resolve the account nonce
	value := opts.Value
	if value == nil {
		value = new(big.Int)
	}
	var nonce uint64
	if opts.Nonce == nil {
		nonce, err = c.transactor.PendingNonceAt(ensureContext(opts.Context), opts.From)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve account nonce: %v", err)
		}
	} else {
		nonce = opts.Nonce.Uint64()
	}
	// Figure out the gas allowance and gas price values
	gasPrice := opts.GasPrice
	if gasPrice == nil {
		gasPrice, err = c.transactor.SuggestGasPrice(ensureContext(opts.Context))
		if err != nil {
			return nil, fmt.Errorf("failed to suggest gas price: %v", err)
		}
	}
	gasLimit := opts.GasLimit
	if gasLimit == 0 {
		// Gas estimation cannot succeed without code for method invocations
		if contract != nil {
			if code, err := c.transactor.PendingCodeAt(ensureContext(opts.Context), c.address); err != nil {
				return nil, err
			} else if len(code) == 0 {
				return nil, ErrNoCode
			}
		}
		// If the contract surely has code (or code is not needed), estimate the transaction
		msg := canchain.CallMsg{From: opts.From, To: contract, Value: value, Data: input}
		gasLimit, err = c.transactor.EstimateGas(ensureContext(opts.Context), msg)
		if err != nil {
			return nil, fmt.Errorf("failed to estimate gas needed: %v", err)
		}
	}
	// Create the transaction, sign it and schedule it for execution
	var rawTx *types.Transaction
	if contract == nil {
		rawTx = types.NewContractCreation(nonce, value, gasLimit, gasPrice, input)
	} else {
		rawTx = types.NewTransaction(nonce, c.address, value, gasLimit, gasPrice, input)
	}
	if opts.Signer == nil {
		return nil, errors.New("no signer to authorize the transaction with")
	}
	signedTx, err := opts.Signer(types.HomesteadSigner{}, opts.From, rawTx)
	if err != nil {
		return nil, err
	}
	if err := c.transactor.SendTransaction(ensureContext(opts.Context), signedTx); err != nil {
		return nil, err
	}
	return signedTx, nil
}

func (c *BoundContract) FilterLogs(opts *FilterOpts, name string, query ...[]interface{}) (chan types.Log, event.Subscription, error) {
	// Don't crash on a lazy user
	if opts == nil {
		opts = new(FilterOpts)
	}
	// Append the event selector to the query parameters and construct the topic set
	query = append([][]interface{}{{c.abi.Events[name].Id()}}, query...)

	topics, err := makeTopics(query...)
	if err != nil {
		return nil, nil, err
	}
	// Start the background filtering
	logs := make(chan types.Log, 128)

	config := canchain.FilterQuery{
		Addresses: []common.Address{c.address},
		Topics:    topics,
		FromBlock: new(big.Int).SetUint64(opts.Start),
	}
	if opts.End != nil {
		config.ToBlock = new(big.Int).SetUint64(*opts.End)
	}
	/* TODO(karalabe): Replace the rest of the method below with this when supported
	sub, err := c.filterer.SubscribeFilterLogs(ensureContext(opts.Context), config, logs)
	*/
	buff, err := c.filterer.FilterLogs(ensureContext(opts.Context), config)
	if err != nil {
		return nil, nil, err
	}
	sub, err := event.NewSubscription(func(quit <-chan struct{}) error {
		for _, log := range buff {
			select {
			case logs <- log:
			case <-quit:
				return nil
			}
		}
		return nil
	}), nil

	if err != nil {
		return nil, nil, err
	}
	return logs, sub, nil
}

func (c *BoundContract) WatchLogs(opts *WatchOpts, name string, query ...[]interface{}) (chan types.Log, event.Subscription, error) {
	// Don't crash on a lazy user
	if opts == nil {
		opts = new(WatchOpts)
	}
	// Append the event selector to the query parameters and construct the topic set
	query = append([][]interface{}{{c.abi.Events[name].Id()}}, query...)

	topics, err := makeTopics(query...)
	if err != nil {
		return nil, nil, err
	}
	// Start the background filtering
	logs := make(chan types.Log, 128)

	config := canchain.FilterQuery{
		Addresses: []common.Address{c.address},
		Topics:    topics,
	}
	if opts.Start != nil {
		config.FromBlock = new(big.Int).SetUint64(*opts.Start)
	}
	sub, err := c.filterer.SubscribeFilterLogs(ensureContext(opts.Context), config, logs)
	if err != nil {
		return nil, nil, err
	}
	return logs, sub, nil
}

func (c *BoundContract) UnpackLog(out interface{}, event string, log types.Log) error {
	if len(log.Data) > 0 {
		if err := c.abi.Unpack(out, event, log.Data); err != nil {
			return err
		}
	}
	var indexed abi.Arguments
	for _, arg := range c.abi.Events[event].Inputs {
		if arg.Indexed {
			indexed = append(indexed, arg)
		}
	}
	return parseTopics(out, indexed, log.Topics[1:])
}

func ensureContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.TODO()
	}
	return ctx
}
