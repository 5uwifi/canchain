//
// (at your option) any later version.
//
//

package usbwallet

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"sync"
	"time"

	ethereum "github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/karalabe/hid"
)

const heartbeatCycle = time.Second

const selfDeriveThrottling = time.Second

type driver interface {
	// Status returns a textual status to aid the user in the current state of the
	// wallet. It also returns an error indicating any failure the wallet might have
	// encountered.
	Status() (string, error)

	// Open initializes access to a wallet instance. The passphrase parameter may
	// or may not be used by the implementation of a particular wallet instance.
	Open(device io.ReadWriter, passphrase string) error

	// Close releases any resources held by an open wallet instance.
	Close() error

	// Heartbeat performs a sanity check against the hardware wallet to see if it
	// is still online and healthy.
	Heartbeat() error

	// Derive sends a derivation request to the USB device and returns the Ethereum
	// address located on that path.
	Derive(path accounts.DerivationPath) (common.Address, error)

	// SignTx sends the transaction to the USB device and waits for the user to confirm
	// or deny the transaction.
	SignTx(path accounts.DerivationPath, tx *types.Transaction, chainID *big.Int) (common.Address, *types.Transaction, error)
}

type wallet struct {
	hub    *Hub          // USB hub scanning
	driver driver        // Hardware implementation of the low level device operations
	url    *accounts.URL // Textual URL uniquely identifying this wallet

	info   hid.DeviceInfo // Known USB device infos about the wallet
	device *hid.Device    // USB device advertising itself as a hardware wallet

	accounts []accounts.Account                         // List of derive accounts pinned on the hardware wallet
	paths    map[common.Address]accounts.DerivationPath // Known derivation paths for signing operations

	deriveNextPath accounts.DerivationPath   // Next derivation path for account auto-discovery
	deriveNextAddr common.Address            // Next derived account address for auto-discovery
	deriveChain    ethereum.ChainStateReader // Blockchain state reader to discover used account with
	deriveReq      chan chan struct{}        // Channel to request a self-derivation on
	deriveQuit     chan chan error           // Channel to terminate the self-deriver with

	healthQuit chan chan error

	// Locking a hardware wallet is a bit special. Since hardware devices are lower
	// performing, any communication with them might take a non negligible amount of
	// time. Worse still, waiting for user confirmation can take arbitrarily long,
	// but exclusive communication must be upheld during. Locking the entire wallet
	// in the mean time however would stall any parts of the system that don't want
	// to communicate, just read some state (e.g. list the accounts).
	//
	// As such, a hardware wallet needs two locks to function correctly. A state
	// lock can be used to protect the wallet's software-side internal state, which
	// must not be held exclusively during hardware communication. A communication
	// lock can be used to achieve exclusive access to the device itself, this one
	// however should allow "skipping" waiting for operations that might want to
	// use the device, but can live without too (e.g. account self-derivation).
	//
	// Since we have two locks, it's important to know how to properly use them:
	//   - Communication requires the `device` to not change, so obtaining the
	//     commsLock should be done after having a stateLock.
	//   - Communication must not disable read access to the wallet state, so it
	//     must only ever hold a *read* lock to stateLock.
	commsLock chan struct{} // Mutex (buf=1) for the USB comms without keeping the state locked
	stateLock sync.RWMutex  // Protects read and write access to the wallet struct fields

	log log4j.Logger // Contextual logger to tag the base with its id
}

func (w *wallet) URL() accounts.URL {
	return *w.url // Immutable, no need for a lock
}

func (w *wallet) Status() (string, error) {
	w.stateLock.RLock() // No device communication, state lock is enough
	defer w.stateLock.RUnlock()

	status, failure := w.driver.Status()
	if w.device == nil {
		return "Closed", failure
	}
	return status, failure
}

func (w *wallet) Open(passphrase string) error {
	w.stateLock.Lock() // State lock is enough since there's no connection yet at this point
	defer w.stateLock.Unlock()

	// If the device was already opened once, refuse to try again
	if w.paths != nil {
		return accounts.ErrWalletAlreadyOpen
	}
	// Make sure the actual device connection is done only once
	if w.device == nil {
		device, err := w.info.Open()
		if err != nil {
			return err
		}
		w.device = device
		w.commsLock = make(chan struct{}, 1)
		w.commsLock <- struct{}{} // Enable lock
	}
	// Delegate device initialization to the underlying driver
	if err := w.driver.Open(w.device, passphrase); err != nil {
		return err
	}
	// Connection successful, start life-cycle management
	w.paths = make(map[common.Address]accounts.DerivationPath)

	w.deriveReq = make(chan chan struct{})
	w.deriveQuit = make(chan chan error)
	w.healthQuit = make(chan chan error)

	go w.heartbeat()
	go w.selfDerive()

	// Notify anyone listening for wallet events that a new device is accessible
	go w.hub.updateFeed.Send(accounts.WalletEvent{Wallet: w, Kind: accounts.WalletOpened})

	return nil
}

func (w *wallet) heartbeat() {
	w.log.Debug("USB wallet health-check started")
	defer w.log.Debug("USB wallet health-check stopped")

	// Execute heartbeat checks until termination or error
	var (
		errc chan error
		err  error
	)
	for errc == nil && err == nil {
		// Wait until termination is requested or the heartbeat cycle arrives
		select {
		case errc = <-w.healthQuit:
			// Termination requested
			continue
		case <-time.After(heartbeatCycle):
			// Heartbeat time
		}
		// Execute a tiny data exchange to see responsiveness
		w.stateLock.RLock()
		if w.device == nil {
			// Terminated while waiting for the lock
			w.stateLock.RUnlock()
			continue
		}
		<-w.commsLock // Don't lock state while resolving version
		err = w.driver.Heartbeat()
		w.commsLock <- struct{}{}
		w.stateLock.RUnlock()

		if err != nil {
			w.stateLock.Lock() // Lock state to tear the wallet down
			w.close()
			w.stateLock.Unlock()
		}
		// Ignore non hardware related errors
		err = nil
	}
	// In case of error, wait for termination
	if err != nil {
		w.log.Debug("USB wallet health-check failed", "err", err)
		errc = <-w.healthQuit
	}
	errc <- err
}

func (w *wallet) Close() error {
	// Ensure the wallet was opened
	w.stateLock.RLock()
	hQuit, dQuit := w.healthQuit, w.deriveQuit
	w.stateLock.RUnlock()

	// Terminate the health checks
	var herr error
	if hQuit != nil {
		errc := make(chan error)
		hQuit <- errc
		herr = <-errc // Save for later, we *must* close the USB
	}
	// Terminate the self-derivations
	var derr error
	if dQuit != nil {
		errc := make(chan error)
		dQuit <- errc
		derr = <-errc // Save for later, we *must* close the USB
	}
	// Terminate the device connection
	w.stateLock.Lock()
	defer w.stateLock.Unlock()

	w.healthQuit = nil
	w.deriveQuit = nil
	w.deriveReq = nil

	if err := w.close(); err != nil {
		return err
	}
	if herr != nil {
		return herr
	}
	return derr
}

//
func (w *wallet) close() error {
	// Allow duplicate closes, especially for health-check failures
	if w.device == nil {
		return nil
	}
	// Close the device, clear everything, then return
	w.device.Close()
	w.device = nil

	w.accounts, w.paths = nil, nil
	w.driver.Close()

	return nil
}

func (w *wallet) Accounts() []accounts.Account {
	// Attempt self-derivation if it's running
	reqc := make(chan struct{}, 1)
	select {
	case w.deriveReq <- reqc:
		// Self-derivation request accepted, wait for it
		<-reqc
	default:
		// Self-derivation offline, throttled or busy, skip
	}
	// Return whatever account list we ended up with
	w.stateLock.RLock()
	defer w.stateLock.RUnlock()

	cpy := make([]accounts.Account, len(w.accounts))
	copy(cpy, w.accounts)
	return cpy
}

func (w *wallet) selfDerive() {
	w.log.Debug("USB wallet self-derivation started")
	defer w.log.Debug("USB wallet self-derivation stopped")

	// Execute self-derivations until termination or error
	var (
		reqc chan struct{}
		errc chan error
		err  error
	)
	for errc == nil && err == nil {
		// Wait until either derivation or termination is requested
		select {
		case errc = <-w.deriveQuit:
			// Termination requested
			continue
		case reqc = <-w.deriveReq:
			// Account discovery requested
		}
		// Derivation needs a chain and device access, skip if either unavailable
		w.stateLock.RLock()
		if w.device == nil || w.deriveChain == nil {
			w.stateLock.RUnlock()
			reqc <- struct{}{}
			continue
		}
		select {
		case <-w.commsLock:
		default:
			w.stateLock.RUnlock()
			reqc <- struct{}{}
			continue
		}
		// Device lock obtained, derive the next batch of accounts
		var (
			accs  []accounts.Account
			paths []accounts.DerivationPath

			nextAddr = w.deriveNextAddr
			nextPath = w.deriveNextPath

			context = context.Background()
		)
		for empty := false; !empty; {
			// Retrieve the next derived Ethereum account
			if nextAddr == (common.Address{}) {
				if nextAddr, err = w.driver.Derive(nextPath); err != nil {
					w.log.Warn("USB wallet account derivation failed", "err", err)
					break
				}
			}
			// Check the account's status against the current chain state
			var (
				balance *big.Int
				nonce   uint64
			)
			balance, err = w.deriveChain.BalanceAt(context, nextAddr, nil)
			if err != nil {
				w.log.Warn("USB wallet balance retrieval failed", "err", err)
				break
			}
			nonce, err = w.deriveChain.NonceAt(context, nextAddr, nil)
			if err != nil {
				w.log.Warn("USB wallet nonce retrieval failed", "err", err)
				break
			}
			// If the next account is empty, stop self-derivation, but add it nonetheless
			if balance.Sign() == 0 && nonce == 0 {
				empty = true
			}
			// We've just self-derived a new account, start tracking it locally
			path := make(accounts.DerivationPath, len(nextPath))
			copy(path[:], nextPath[:])
			paths = append(paths, path)

			account := accounts.Account{
				Address: nextAddr,
				URL:     accounts.URL{Scheme: w.url.Scheme, Path: fmt.Sprintf("%s/%s", w.url.Path, path)},
			}
			accs = append(accs, account)

			// Display a log message to the user for new (or previously empty accounts)
			if _, known := w.paths[nextAddr]; !known || (!empty && nextAddr == w.deriveNextAddr) {
				w.log.Info("USB wallet discovered new account", "address", nextAddr, "path", path, "balance", balance, "nonce", nonce)
			}
			// Fetch the next potential account
			if !empty {
				nextAddr = common.Address{}
				nextPath[len(nextPath)-1]++
			}
		}
		// Self derivation complete, release device lock
		w.commsLock <- struct{}{}
		w.stateLock.RUnlock()

		// Insert any accounts successfully derived
		w.stateLock.Lock()
		for i := 0; i < len(accs); i++ {
			if _, ok := w.paths[accs[i].Address]; !ok {
				w.accounts = append(w.accounts, accs[i])
				w.paths[accs[i].Address] = paths[i]
			}
		}
		// Shift the self-derivation forward
		// TODO(karalabe): don't overwrite changes from wallet.SelfDerive
		w.deriveNextAddr = nextAddr
		w.deriveNextPath = nextPath
		w.stateLock.Unlock()

		// Notify the user of termination and loop after a bit of time (to avoid trashing)
		reqc <- struct{}{}
		if err == nil {
			select {
			case errc = <-w.deriveQuit:
				// Termination requested, abort
			case <-time.After(selfDeriveThrottling):
				// Waited enough, willing to self-derive again
			}
		}
	}
	// In case of error, wait for termination
	if err != nil {
		w.log.Debug("USB wallet self-derivation failed", "err", err)
		errc = <-w.deriveQuit
	}
	errc <- err
}

func (w *wallet) Contains(account accounts.Account) bool {
	w.stateLock.RLock()
	defer w.stateLock.RUnlock()

	_, exists := w.paths[account.Address]
	return exists
}

func (w *wallet) Derive(path accounts.DerivationPath, pin bool) (accounts.Account, error) {
	// Try to derive the actual account and update its URL if successful
	w.stateLock.RLock() // Avoid device disappearing during derivation

	if w.device == nil {
		w.stateLock.RUnlock()
		return accounts.Account{}, accounts.ErrWalletClosed
	}
	<-w.commsLock // Avoid concurrent hardware access
	address, err := w.driver.Derive(path)
	w.commsLock <- struct{}{}

	w.stateLock.RUnlock()

	// If an error occurred or no pinning was requested, return
	if err != nil {
		return accounts.Account{}, err
	}
	account := accounts.Account{
		Address: address,
		URL:     accounts.URL{Scheme: w.url.Scheme, Path: fmt.Sprintf("%s/%s", w.url.Path, path)},
	}
	if !pin {
		return account, nil
	}
	// Pinning needs to modify the state
	w.stateLock.Lock()
	defer w.stateLock.Unlock()

	if _, ok := w.paths[address]; !ok {
		w.accounts = append(w.accounts, account)
		w.paths[address] = path
	}
	return account, nil
}

func (w *wallet) SelfDerive(base accounts.DerivationPath, chain ethereum.ChainStateReader) {
	w.stateLock.Lock()
	defer w.stateLock.Unlock()

	w.deriveNextPath = make(accounts.DerivationPath, len(base))
	copy(w.deriveNextPath[:], base[:])

	w.deriveNextAddr = common.Address{}
	w.deriveChain = chain
}

func (w *wallet) SignHash(account accounts.Account, hash []byte) ([]byte, error) {
	return nil, accounts.ErrNotSupported
}

//
func (w *wallet) SignTx(account accounts.Account, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	w.stateLock.RLock() // Comms have own mutex, this is for the state fields
	defer w.stateLock.RUnlock()

	// If the wallet is closed, abort
	if w.device == nil {
		return nil, accounts.ErrWalletClosed
	}
	// Make sure the requested account is contained within
	path, ok := w.paths[account.Address]
	if !ok {
		return nil, accounts.ErrUnknownAccount
	}
	// All infos gathered and metadata checks out, request signing
	<-w.commsLock
	defer func() { w.commsLock <- struct{}{} }()

	// Ensure the device isn't screwed with while user confirmation is pending
	// TODO(karalabe): remove if hotplug lands on Windows
	w.hub.commsLock.Lock()
	w.hub.commsPend++
	w.hub.commsLock.Unlock()

	defer func() {
		w.hub.commsLock.Lock()
		w.hub.commsPend--
		w.hub.commsLock.Unlock()
	}()
	// Sign the transaction and verify the sender to avoid hardware fault surprises
	sender, signed, err := w.driver.SignTx(path, tx, chainID)
	if err != nil {
		return nil, err
	}
	if sender != account.Address {
		return nil, fmt.Errorf("signer mismatch: expected %s, got %s", account.Address.Hex(), sender.Hex())
	}
	return signed, nil
}

func (w *wallet) SignHashWithPassphrase(account accounts.Account, passphrase string, hash []byte) ([]byte, error) {
	return w.SignHash(account, hash)
}

func (w *wallet) SignTxWithPassphrase(account accounts.Account, passphrase string, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	return w.SignTx(account, tx, chainID)
}
