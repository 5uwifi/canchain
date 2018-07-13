package chequebook

//go:generate abigen --sol contract/chequebook.sol --exc contract/mortal.sol:mortal,contract/owned.sol:owned --pkg contract --out contract/chequebook.go
//go:generate go run ./gencode.go

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/5uwifi/canchain/accounts/abi/bind"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/contracts/chequebook/contract"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/basis/crypto"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/swarm/services/swap/swap"
)

// * setting current balance on peer's chequebook
// * sending the transaction to cash the cheque
// * depositing ether to the chequebook
// * watching incoming ether

var (
	gasToCash = uint64(2000000) // gas cost of a cash transaction using chequebook
	// gasToDeploy = uint64(3000000)
)

type Backend interface {
	bind.ContractBackend
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
	BalanceAt(ctx context.Context, address common.Address, blockNum *big.Int) (*big.Int, error)
}

type Cheque struct {
	Contract    common.Address // address of chequebook, needed to avoid cross-contract submission
	Beneficiary common.Address
	Amount      *big.Int // cumulative amount of all funds sent
	Sig         []byte   // signature Sign(Keccak256(contract, beneficiary, amount), prvKey)
}

func (self *Cheque) String() string {
	return fmt.Sprintf("contract: %s, beneficiary: %s, amount: %v, signature: %x", self.Contract.Hex(), self.Beneficiary.Hex(), self.Amount, self.Sig)
}

type Params struct {
	ContractCode, ContractAbi string
}

var ContractParams = &Params{contract.ChequebookBin, contract.ChequebookABI}

type Chequebook struct {
	path     string                      // path to chequebook file
	prvKey   *ecdsa.PrivateKey           // private key to sign cheque with
	lock     sync.Mutex                  //
	backend  Backend                     // blockchain API
	quit     chan bool                   // when closed causes autodeposit to stop
	owner    common.Address              // owner address (derived from pubkey)
	contract *contract.Chequebook        // abigen binding
	session  *contract.ChequebookSession // abigen binding with Tx Opts

	// persisted fields
	balance      *big.Int                    // not synced with blockchain
	contractAddr common.Address              // contract address
	sent         map[common.Address]*big.Int //tallies for beneficiaries

	txhash    string   // tx hash of last deposit tx
	threshold *big.Int // threshold that triggers autodeposit if not nil
	buffer    *big.Int // buffer to keep on top of balance for fork protection

	log log4j.Logger // contextual logger with the contract address embedded
}

func (self *Chequebook) String() string {
	return fmt.Sprintf("contract: %s, owner: %s, balance: %v, signer: %x", self.contractAddr.Hex(), self.owner.Hex(), self.balance, self.prvKey.PublicKey)
}

func NewChequebook(path string, contractAddr common.Address, prvKey *ecdsa.PrivateKey, backend Backend) (self *Chequebook, err error) {
	balance := new(big.Int)
	sent := make(map[common.Address]*big.Int)

	chbook, err := contract.NewChequebook(contractAddr, backend)
	if err != nil {
		return nil, err
	}
	transactOpts := bind.NewKeyedTransactor(prvKey)
	session := &contract.ChequebookSession{
		Contract:     chbook,
		TransactOpts: *transactOpts,
	}

	self = &Chequebook{
		prvKey:       prvKey,
		balance:      balance,
		contractAddr: contractAddr,
		sent:         sent,
		path:         path,
		backend:      backend,
		owner:        transactOpts.From,
		contract:     chbook,
		session:      session,
		log:          log4j.New("contract", contractAddr),
	}

	if (contractAddr != common.Address{}) {
		self.setBalanceFromBlockChain()
		self.log.Trace("New chequebook initialised", "owner", self.owner, "balance", self.balance)
	}
	return
}

func (self *Chequebook) setBalanceFromBlockChain() {
	balance, err := self.backend.BalanceAt(context.TODO(), self.contractAddr, nil)
	if err != nil {
		log4j.Error("Failed to retrieve chequebook balance", "err", err)
	} else {
		self.balance.Set(balance)
	}
}

func LoadChequebook(path string, prvKey *ecdsa.PrivateKey, backend Backend, checkBalance bool) (self *Chequebook, err error) {
	var data []byte
	data, err = ioutil.ReadFile(path)
	if err != nil {
		return
	}
	self, _ = NewChequebook(path, common.Address{}, prvKey, backend)

	err = json.Unmarshal(data, self)
	if err != nil {
		return nil, err
	}
	if checkBalance {
		self.setBalanceFromBlockChain()
	}
	log4j.Trace("Loaded chequebook from disk", "path", path)

	return
}

type chequebookFile struct {
	Balance  string
	Contract string
	Owner    string
	Sent     map[string]string
}

func (self *Chequebook) UnmarshalJSON(data []byte) error {
	var file chequebookFile
	err := json.Unmarshal(data, &file)
	if err != nil {
		return err
	}
	_, ok := self.balance.SetString(file.Balance, 10)
	if !ok {
		return fmt.Errorf("cumulative amount sent: unable to convert string to big integer: %v", file.Balance)
	}
	self.contractAddr = common.HexToAddress(file.Contract)
	for addr, sent := range file.Sent {
		self.sent[common.HexToAddress(addr)], ok = new(big.Int).SetString(sent, 10)
		if !ok {
			return fmt.Errorf("beneficiary %v cumulative amount sent: unable to convert string to big integer: %v", addr, sent)
		}
	}
	return nil
}

func (self *Chequebook) MarshalJSON() ([]byte, error) {
	var file = &chequebookFile{
		Balance:  self.balance.String(),
		Contract: self.contractAddr.Hex(),
		Owner:    self.owner.Hex(),
		Sent:     make(map[string]string),
	}
	for addr, sent := range self.sent {
		file.Sent[addr.Hex()] = sent.String()
	}
	return json.Marshal(file)
}

func (self *Chequebook) Save() (err error) {
	data, err := json.MarshalIndent(self, "", " ")
	if err != nil {
		return err
	}
	self.log.Trace("Saving chequebook to disk", self.path)

	return ioutil.WriteFile(self.path, data, os.ModePerm)
}

func (self *Chequebook) Stop() {
	defer self.lock.Unlock()
	self.lock.Lock()
	if self.quit != nil {
		close(self.quit)
		self.quit = nil
	}
}

func (self *Chequebook) Issue(beneficiary common.Address, amount *big.Int) (ch *Cheque, err error) {
	defer self.lock.Unlock()
	self.lock.Lock()

	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be greater than zero (%v)", amount)
	}
	if self.balance.Cmp(amount) < 0 {
		err = fmt.Errorf("insufficient funds to issue cheque for amount: %v. balance: %v", amount, self.balance)
	} else {
		var sig []byte
		sent, found := self.sent[beneficiary]
		if !found {
			sent = new(big.Int)
			self.sent[beneficiary] = sent
		}
		sum := new(big.Int).Set(sent)
		sum.Add(sum, amount)

		sig, err = crypto.Sign(sigHash(self.contractAddr, beneficiary, sum), self.prvKey)
		if err == nil {
			ch = &Cheque{
				Contract:    self.contractAddr,
				Beneficiary: beneficiary,
				Amount:      sum,
				Sig:         sig,
			}
			sent.Set(sum)
			self.balance.Sub(self.balance, amount) // subtract amount from balance
		}
	}

	// auto deposit if threshold is set and balance is less then threshold
	// note this is called even if issuing cheque fails
	// so we reattempt depositing
	if self.threshold != nil {
		if self.balance.Cmp(self.threshold) < 0 {
			send := new(big.Int).Sub(self.buffer, self.balance)
			self.deposit(send)
		}
	}

	return
}

func (self *Chequebook) Cash(ch *Cheque) (txhash string, err error) {
	return ch.Cash(self.session)
}

func sigHash(contract, beneficiary common.Address, sum *big.Int) []byte {
	bigamount := sum.Bytes()
	if len(bigamount) > 32 {
		return nil
	}
	var amount32 [32]byte
	copy(amount32[32-len(bigamount):32], bigamount)
	input := append(contract.Bytes(), beneficiary.Bytes()...)
	input = append(input, amount32[:]...)
	return crypto.Keccak256(input)
}

func (self *Chequebook) Balance() *big.Int {
	defer self.lock.Unlock()
	self.lock.Lock()
	return new(big.Int).Set(self.balance)
}

func (self *Chequebook) Owner() common.Address {
	return self.owner
}

func (self *Chequebook) Address() common.Address {
	return self.contractAddr
}

func (self *Chequebook) Deposit(amount *big.Int) (string, error) {
	defer self.lock.Unlock()
	self.lock.Lock()
	return self.deposit(amount)
}

func (self *Chequebook) deposit(amount *big.Int) (string, error) {
	// since the amount is variable here, we do not use sessions
	depositTransactor := bind.NewKeyedTransactor(self.prvKey)
	depositTransactor.Value = amount
	chbookRaw := &contract.ChequebookRaw{Contract: self.contract}
	tx, err := chbookRaw.Transfer(depositTransactor)
	if err != nil {
		self.log.Warn("Failed to fund chequebook", "amount", amount, "balance", self.balance, "target", self.buffer, "err", err)
		return "", err
	}
	// assume that transaction is actually successful, we add the amount to balance right away
	self.balance.Add(self.balance, amount)
	self.log.Trace("Deposited funds to chequebook", "amount", amount, "balance", self.balance, "target", self.buffer)
	return tx.Hash().Hex(), nil
}

func (self *Chequebook) AutoDeposit(interval time.Duration, threshold, buffer *big.Int) {
	defer self.lock.Unlock()
	self.lock.Lock()
	self.threshold = threshold
	self.buffer = buffer
	self.autoDeposit(interval)
}

func (self *Chequebook) autoDeposit(interval time.Duration) {
	if self.quit != nil {
		close(self.quit)
		self.quit = nil
	}
	// if threshold >= balance autodeposit after every cheque issued
	if interval == time.Duration(0) || self.threshold != nil && self.buffer != nil && self.threshold.Cmp(self.buffer) >= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	self.quit = make(chan bool)
	quit := self.quit

	go func() {
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				self.lock.Lock()
				if self.balance.Cmp(self.buffer) < 0 {
					amount := new(big.Int).Sub(self.buffer, self.balance)
					txhash, err := self.deposit(amount)
					if err == nil {
						self.txhash = txhash
					}
				}
				self.lock.Unlock()
			}
		}
	}()
}

type Outbox struct {
	chequeBook  *Chequebook
	beneficiary common.Address
}

func NewOutbox(chbook *Chequebook, beneficiary common.Address) *Outbox {
	return &Outbox{chbook, beneficiary}
}

func (self *Outbox) Issue(amount *big.Int) (swap.Promise, error) {
	return self.chequeBook.Issue(self.beneficiary, amount)
}

func (self *Outbox) AutoDeposit(interval time.Duration, threshold, buffer *big.Int) {
	self.chequeBook.AutoDeposit(interval, threshold, buffer)
}

func (self *Outbox) Stop() {}

func (self *Outbox) String() string {
	return fmt.Sprintf("chequebook: %v, beneficiary: %s, balance: %v", self.chequeBook.Address().Hex(), self.beneficiary.Hex(), self.chequeBook.Balance())
}

type Inbox struct {
	lock        sync.Mutex
	contract    common.Address              // peer's chequebook contract
	beneficiary common.Address              // local peer's receiving address
	sender      common.Address              // local peer's address to send cashing tx from
	signer      *ecdsa.PublicKey            // peer's public key
	txhash      string                      // tx hash of last cashing tx
	session     *contract.ChequebookSession // abi contract backend with tx opts
	quit        chan bool                   // when closed causes autocash to stop
	maxUncashed *big.Int                    // threshold that triggers autocashing
	cashed      *big.Int                    // cumulative amount cashed
	cheque      *Cheque                     // last cheque, nil if none yet received
	log         log4j.Logger                // contextual logger with the contract address embedded
}

func NewInbox(prvKey *ecdsa.PrivateKey, contractAddr, beneficiary common.Address, signer *ecdsa.PublicKey, abigen bind.ContractBackend) (self *Inbox, err error) {
	if signer == nil {
		return nil, fmt.Errorf("signer is null")
	}
	chbook, err := contract.NewChequebook(contractAddr, abigen)
	if err != nil {
		return nil, err
	}
	transactOpts := bind.NewKeyedTransactor(prvKey)
	transactOpts.GasLimit = gasToCash
	session := &contract.ChequebookSession{
		Contract:     chbook,
		TransactOpts: *transactOpts,
	}
	sender := transactOpts.From

	self = &Inbox{
		contract:    contractAddr,
		beneficiary: beneficiary,
		sender:      sender,
		signer:      signer,
		session:     session,
		cashed:      new(big.Int).Set(common.Big0),
		log:         log4j.New("contract", contractAddr),
	}
	self.log.Trace("New chequebook inbox initialized", "beneficiary", self.beneficiary, "signer", hexutil.Bytes(crypto.FromECDSAPub(signer)))
	return
}

func (self *Inbox) String() string {
	return fmt.Sprintf("chequebook: %v, beneficiary: %s, balance: %v", self.contract.Hex(), self.beneficiary.Hex(), self.cheque.Amount)
}

func (self *Inbox) Stop() {
	defer self.lock.Unlock()
	self.lock.Lock()
	if self.quit != nil {
		close(self.quit)
		self.quit = nil
	}
}

func (self *Inbox) Cash() (txhash string, err error) {
	if self.cheque != nil {
		txhash, err = self.cheque.Cash(self.session)
		self.log.Trace("Cashing in chequebook cheque", "amount", self.cheque.Amount, "beneficiary", self.beneficiary)
		self.cashed = self.cheque.Amount
	}
	return
}

func (self *Inbox) AutoCash(cashInterval time.Duration, maxUncashed *big.Int) {
	defer self.lock.Unlock()
	self.lock.Lock()
	self.maxUncashed = maxUncashed
	self.autoCash(cashInterval)
}

func (self *Inbox) autoCash(cashInterval time.Duration) {
	if self.quit != nil {
		close(self.quit)
		self.quit = nil
	}
	// if maxUncashed is set to 0, then autocash on receipt
	if cashInterval == time.Duration(0) || self.maxUncashed != nil && self.maxUncashed.Sign() == 0 {
		return
	}

	ticker := time.NewTicker(cashInterval)
	self.quit = make(chan bool)
	quit := self.quit

	go func() {
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				self.lock.Lock()
				if self.cheque != nil && self.cheque.Amount.Cmp(self.cashed) != 0 {
					txhash, err := self.Cash()
					if err == nil {
						self.txhash = txhash
					}
				}
				self.lock.Unlock()
			}
		}
	}()
}

func (self *Inbox) Receive(promise swap.Promise) (*big.Int, error) {
	ch := promise.(*Cheque)

	defer self.lock.Unlock()
	self.lock.Lock()

	var sum *big.Int
	if self.cheque == nil {
		// the sum is checked against the blockchain once a cheque is received
		tally, err := self.session.Sent(self.beneficiary)
		if err != nil {
			return nil, fmt.Errorf("inbox: error calling backend to set amount: %v", err)
		}
		sum = tally
	} else {
		sum = self.cheque.Amount
	}

	amount, err := ch.Verify(self.signer, self.contract, self.beneficiary, sum)
	var uncashed *big.Int
	if err == nil {
		self.cheque = ch

		if self.maxUncashed != nil {
			uncashed = new(big.Int).Sub(ch.Amount, self.cashed)
			if self.maxUncashed.Cmp(uncashed) < 0 {
				self.Cash()
			}
		}
		self.log.Trace("Received cheque in chequebook inbox", "amount", amount, "uncashed", uncashed)
	}

	return amount, err
}

func (self *Cheque) Verify(signerKey *ecdsa.PublicKey, contract, beneficiary common.Address, sum *big.Int) (*big.Int, error) {
	log4j.Trace("Verifying chequebook cheque", "cheque", self, "sum", sum)
	if sum == nil {
		return nil, fmt.Errorf("invalid amount")
	}

	if self.Beneficiary != beneficiary {
		return nil, fmt.Errorf("beneficiary mismatch: %v != %v", self.Beneficiary.Hex(), beneficiary.Hex())
	}
	if self.Contract != contract {
		return nil, fmt.Errorf("contract mismatch: %v != %v", self.Contract.Hex(), contract.Hex())
	}

	amount := new(big.Int).Set(self.Amount)
	if sum != nil {
		amount.Sub(amount, sum)
		if amount.Sign() <= 0 {
			return nil, fmt.Errorf("incorrect amount: %v <= 0", amount)
		}
	}

	pubKey, err := crypto.SigToPub(sigHash(self.Contract, beneficiary, self.Amount), self.Sig)
	if err != nil {
		return nil, fmt.Errorf("invalid signature: %v", err)
	}
	if !bytes.Equal(crypto.FromECDSAPub(pubKey), crypto.FromECDSAPub(signerKey)) {
		return nil, fmt.Errorf("signer mismatch: %x != %x", crypto.FromECDSAPub(pubKey), crypto.FromECDSAPub(signerKey))
	}
	return amount, nil
}

func sig2vrs(sig []byte) (v byte, r, s [32]byte) {
	v = sig[64] + 27
	copy(r[:], sig[:32])
	copy(s[:], sig[32:64])
	return
}

func (self *Cheque) Cash(session *contract.ChequebookSession) (string, error) {
	v, r, s := sig2vrs(self.Sig)
	tx, err := session.Cash(self.Beneficiary, self.Amount, v, r, s)
	if err != nil {
		return "", err
	}
	return tx.Hash().Hex(), nil
}

func ValidateCode(ctx context.Context, b Backend, address common.Address) (ok bool, err error) {
	code, err := b.CodeAt(ctx, address, nil)
	if err != nil {
		return false, err
	}
	return bytes.Equal(code, common.FromHex(contract.ContractDeployedCode)), nil
}
