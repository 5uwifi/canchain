
package contract

import (
	"strings"

	"github.com/5uwifi/canchain/accounts/abi"
	"github.com/5uwifi/canchain/accounts/abi/bind"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
)

const FIFSRegistrarABI = "[{\"constant\":false,\"inputs\":[{\"name\":\"subnode\",\"type\":\"bytes32\"},{\"name\":\"owner\",\"type\":\"address\"}],\"name\":\"register\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"name\":\"ensAddr\",\"type\":\"address\"},{\"name\":\"node\",\"type\":\"bytes32\"}],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"}]"

const FIFSRegistrarBin = `0x6060604052341561000f57600080fd5b604051604080610224833981016040528080519190602001805160008054600160a060020a03909516600160a060020a03199095169490941790935550506001556101c58061005f6000396000f3006060604052600436106100275763ffffffff60e060020a600035041663d22057a9811461002c575b600080fd5b341561003757600080fd5b61004e600435600160a060020a0360243516610050565b005b816000806001548360405191825260208201526040908101905190819003902060008054919350600160a060020a03909116906302571be39084906040516020015260405160e060020a63ffffffff84160281526004810191909152602401602060405180830381600087803b15156100c857600080fd5b6102c65a03f115156100d957600080fd5b5050506040518051915050600160a060020a0381161580159061010e575033600160a060020a031681600160a060020a031614155b1561011857600080fd5b600054600154600160a060020a03909116906306ab592390878760405160e060020a63ffffffff861602815260048101939093526024830191909152600160a060020a03166044820152606401600060405180830381600087803b151561017e57600080fd5b6102c65a03f1151561018f57600080fd5b50505050505050505600a165627a7a723058206fb963cb168d5e3a51af12cd6bb23e324dbd32dd4954f43653ba27e66b68ea650029`

func DeployFIFSRegistrar(auth *bind.TransactOpts, backend bind.ContractBackend, ensAddr common.Address, node [32]byte) (common.Address, *types.Transaction, *FIFSRegistrar, error) {
	parsed, err := abi.JSON(strings.NewReader(FIFSRegistrarABI))
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	address, tx, contract, err := bind.DeployContract(auth, parsed, common.FromHex(FIFSRegistrarBin), backend, ensAddr, node)
	if err != nil {
		return common.Address{}, nil, nil, err
	}
	return address, tx, &FIFSRegistrar{FIFSRegistrarCaller: FIFSRegistrarCaller{contract: contract}, FIFSRegistrarTransactor: FIFSRegistrarTransactor{contract: contract}, FIFSRegistrarFilterer: FIFSRegistrarFilterer{contract: contract}}, nil
}

type FIFSRegistrar struct {
	FIFSRegistrarCaller     // Read-only binding to the contract
	FIFSRegistrarTransactor // Write-only binding to the contract
	FIFSRegistrarFilterer   // Log filterer for contract events
}

type FIFSRegistrarCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

type FIFSRegistrarTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

type FIFSRegistrarFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

type FIFSRegistrarSession struct {
	Contract     *FIFSRegistrar    // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

type FIFSRegistrarCallerSession struct {
	Contract *FIFSRegistrarCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts        // Call options to use throughout this session
}

type FIFSRegistrarTransactorSession struct {
	Contract     *FIFSRegistrarTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts        // Transaction auth options to use throughout this session
}

type FIFSRegistrarRaw struct {
	Contract *FIFSRegistrar // Generic contract binding to access the raw methods on
}

type FIFSRegistrarCallerRaw struct {
	Contract *FIFSRegistrarCaller // Generic read-only contract binding to access the raw methods on
}

type FIFSRegistrarTransactorRaw struct {
	Contract *FIFSRegistrarTransactor // Generic write-only contract binding to access the raw methods on
}

func NewFIFSRegistrar(address common.Address, backend bind.ContractBackend) (*FIFSRegistrar, error) {
	contract, err := bindFIFSRegistrar(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &FIFSRegistrar{FIFSRegistrarCaller: FIFSRegistrarCaller{contract: contract}, FIFSRegistrarTransactor: FIFSRegistrarTransactor{contract: contract}, FIFSRegistrarFilterer: FIFSRegistrarFilterer{contract: contract}}, nil
}

func NewFIFSRegistrarCaller(address common.Address, caller bind.ContractCaller) (*FIFSRegistrarCaller, error) {
	contract, err := bindFIFSRegistrar(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &FIFSRegistrarCaller{contract: contract}, nil
}

func NewFIFSRegistrarTransactor(address common.Address, transactor bind.ContractTransactor) (*FIFSRegistrarTransactor, error) {
	contract, err := bindFIFSRegistrar(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &FIFSRegistrarTransactor{contract: contract}, nil
}

func NewFIFSRegistrarFilterer(address common.Address, filterer bind.ContractFilterer) (*FIFSRegistrarFilterer, error) {
	contract, err := bindFIFSRegistrar(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &FIFSRegistrarFilterer{contract: contract}, nil
}

func bindFIFSRegistrar(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := abi.JSON(strings.NewReader(FIFSRegistrarABI))
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, parsed, caller, transactor, filterer), nil
}

func (_FIFSRegistrar *FIFSRegistrarRaw) Call(opts *bind.CallOpts, result interface{}, method string, params ...interface{}) error {
	return _FIFSRegistrar.Contract.FIFSRegistrarCaller.contract.Call(opts, result, method, params...)
}

func (_FIFSRegistrar *FIFSRegistrarRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FIFSRegistrar.Contract.FIFSRegistrarTransactor.contract.Transfer(opts)
}

func (_FIFSRegistrar *FIFSRegistrarRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _FIFSRegistrar.Contract.FIFSRegistrarTransactor.contract.Transact(opts, method, params...)
}

func (_FIFSRegistrar *FIFSRegistrarCallerRaw) Call(opts *bind.CallOpts, result interface{}, method string, params ...interface{}) error {
	return _FIFSRegistrar.Contract.contract.Call(opts, result, method, params...)
}

func (_FIFSRegistrar *FIFSRegistrarTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FIFSRegistrar.Contract.contract.Transfer(opts)
}

func (_FIFSRegistrar *FIFSRegistrarTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _FIFSRegistrar.Contract.contract.Transact(opts, method, params...)
}

//
func (_FIFSRegistrar *FIFSRegistrarTransactor) Register(opts *bind.TransactOpts, subnode [32]byte, owner common.Address) (*types.Transaction, error) {
	return _FIFSRegistrar.contract.Transact(opts, "register", subnode, owner)
}

//
func (_FIFSRegistrar *FIFSRegistrarSession) Register(subnode [32]byte, owner common.Address) (*types.Transaction, error) {
	return _FIFSRegistrar.Contract.Register(&_FIFSRegistrar.TransactOpts, subnode, owner)
}

//
func (_FIFSRegistrar *FIFSRegistrarTransactorSession) Register(subnode [32]byte, owner common.Address) (*types.Transaction, error) {
	return _FIFSRegistrar.Contract.Register(&_FIFSRegistrar.TransactOpts, subnode, owner)
}
