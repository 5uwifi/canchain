package accounts

import (
	"math/big"

	"github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/event"
)

type Account struct {
	Address common.Address `json:"address"`
	URL     URL            `json:"url"`
}

type Wallet interface {
	URL() URL

	Status() (string, error)

	Open(passphrase string) error

	Close() error

	Accounts() []Account

	Contains(account Account) bool

	Derive(path DerivationPath, pin bool) (Account, error)

	SelfDerive(base DerivationPath, chain canchain.ChainStateReader)

	SignHash(account Account, hash []byte) ([]byte, error)

	SignTx(account Account, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)

	SignHashWithPassphrase(account Account, passphrase string, hash []byte) ([]byte, error)

	SignTxWithPassphrase(account Account, passphrase string, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
}

type Backend interface {
	Wallets() []Wallet

	Subscribe(sink chan<- WalletEvent) event.Subscription
}

type WalletEventType int

const (
	WalletArrived WalletEventType = iota

	WalletOpened

	WalletDropped
)

type WalletEvent struct {
	Wallet Wallet
	Kind   WalletEventType
}
