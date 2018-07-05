//
// (at your option) any later version.
//
//

package ens


import (
	"strings"

	"github.com/5uwifi/canchain/accounts/abi/bind"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/contracts/ens/contract"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/basis/crypto"
)

var (
	MainNetAddress = common.HexToAddress("0x314159265dD8dbb310642f98f50C066173C1259b")
	TestNetAddress = common.HexToAddress("0x112234455c3a32fd11230c42e7bccd4a84e02010")
)

type ENS struct {
	*contract.ENSSession
	contractBackend bind.ContractBackend
}

func NewENS(transactOpts *bind.TransactOpts, contractAddr common.Address, contractBackend bind.ContractBackend) (*ENS, error) {
	ens, err := contract.NewENS(contractAddr, contractBackend)
	if err != nil {
		return nil, err
	}

	return &ENS{
		&contract.ENSSession{
			Contract:     ens,
			TransactOpts: *transactOpts,
		},
		contractBackend,
	}, nil
}

func DeployENS(transactOpts *bind.TransactOpts, contractBackend bind.ContractBackend) (common.Address, *ENS, error) {
	// Deploy the ENS registry.
	ensAddr, _, _, err := contract.DeployENS(transactOpts, contractBackend)
	if err != nil {
		return ensAddr, nil, err
	}

	ens, err := NewENS(transactOpts, ensAddr, contractBackend)
	if err != nil {
		return ensAddr, nil, err
	}

	// Deploy the registrar.
	regAddr, _, _, err := contract.DeployFIFSRegistrar(transactOpts, contractBackend, ensAddr, [32]byte{})
	if err != nil {
		return ensAddr, nil, err
	}
	// Set the registrar as owner of the ENS root.
	if _, err = ens.SetOwner([32]byte{}, regAddr); err != nil {
		return ensAddr, nil, err
	}

	return ensAddr, ens, nil
}

func ensParentNode(name string) (common.Hash, common.Hash) {
	parts := strings.SplitN(name, ".", 2)
	label := crypto.Keccak256Hash([]byte(parts[0]))
	if len(parts) == 1 {
		return [32]byte{}, label
	} else {
		parentNode, parentLabel := ensParentNode(parts[1])
		return crypto.Keccak256Hash(parentNode[:], parentLabel[:]), label
	}
}

func EnsNode(name string) common.Hash {
	parentNode, parentLabel := ensParentNode(name)
	return crypto.Keccak256Hash(parentNode[:], parentLabel[:])
}

func (self *ENS) getResolver(node [32]byte) (*contract.PublicResolverSession, error) {
	resolverAddr, err := self.Resolver(node)
	if err != nil {
		return nil, err
	}

	resolver, err := contract.NewPublicResolver(resolverAddr, self.contractBackend)
	if err != nil {
		return nil, err
	}

	return &contract.PublicResolverSession{
		Contract:     resolver,
		TransactOpts: self.TransactOpts,
	}, nil
}

func (self *ENS) getRegistrar(node [32]byte) (*contract.FIFSRegistrarSession, error) {
	registrarAddr, err := self.Owner(node)
	if err != nil {
		return nil, err
	}

	registrar, err := contract.NewFIFSRegistrar(registrarAddr, self.contractBackend)
	if err != nil {
		return nil, err
	}

	return &contract.FIFSRegistrarSession{
		Contract:     registrar,
		TransactOpts: self.TransactOpts,
	}, nil
}

func (self *ENS) Resolve(name string) (common.Hash, error) {
	node := EnsNode(name)

	resolver, err := self.getResolver(node)
	if err != nil {
		return common.Hash{}, err
	}

	ret, err := resolver.Content(node)
	if err != nil {
		return common.Hash{}, err
	}

	return common.BytesToHash(ret[:]), nil
}

func (self *ENS) Register(name string) (*types.Transaction, error) {
	parentNode, label := ensParentNode(name)
	registrar, err := self.getRegistrar(parentNode)
	if err != nil {
		return nil, err
	}
	return registrar.Contract.Register(&self.TransactOpts, label, self.TransactOpts.From)
}

func (self *ENS) SetContentHash(name string, hash common.Hash) (*types.Transaction, error) {
	node := EnsNode(name)

	resolver, err := self.getResolver(node)
	if err != nil {
		return nil, err
	}

	opts := self.TransactOpts
	opts.GasLimit = 200000
	return resolver.Contract.SetContent(&opts, node, hash)
}
