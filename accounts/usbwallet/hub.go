package usbwallet

import (
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/karalabe/hid"
)

const LedgerScheme = "ledger"

const TrezorScheme = "trezor"

const refreshCycle = time.Second

const refreshThrottling = 500 * time.Millisecond

type Hub struct {
	scheme     string
	vendorID   uint16
	productIDs []uint16
	usageID    uint16
	endpointID int
	makeDriver func(log4j.Logger) driver

	refreshed   time.Time
	wallets     []accounts.Wallet
	updateFeed  event.Feed
	updateScope event.SubscriptionScope
	updating    bool

	quit chan chan error

	stateLock sync.RWMutex

	commsPend int
	commsLock sync.Mutex
}

func NewLedgerHub() (*Hub, error) {
	return newHub(LedgerScheme, 0x2c97, []uint16{0x0000 /* Ledger Blue */, 0x0001 /* Ledger Nano S */}, 0xffa0, 0, newLedgerDriver)
}

func NewTrezorHub() (*Hub, error) {
	return newHub(TrezorScheme, 0x534c, []uint16{0x0001 /* Trezor 1 */}, 0xff00, 0, newTrezorDriver)
}

func newHub(scheme string, vendorID uint16, productIDs []uint16, usageID uint16, endpointID int, makeDriver func(log4j.Logger) driver) (*Hub, error) {
	if !hid.Supported() {
		return nil, errors.New("unsupported platform")
	}
	hub := &Hub{
		scheme:     scheme,
		vendorID:   vendorID,
		productIDs: productIDs,
		usageID:    usageID,
		endpointID: endpointID,
		makeDriver: makeDriver,
		quit:       make(chan chan error),
	}
	hub.refreshWallets()
	return hub, nil
}

func (hub *Hub) Wallets() []accounts.Wallet {
	hub.refreshWallets()

	hub.stateLock.RLock()
	defer hub.stateLock.RUnlock()

	cpy := make([]accounts.Wallet, len(hub.wallets))
	copy(cpy, hub.wallets)
	return cpy
}

func (hub *Hub) refreshWallets() {
	hub.stateLock.RLock()
	elapsed := time.Since(hub.refreshed)
	hub.stateLock.RUnlock()

	if elapsed < refreshThrottling {
		return
	}
	var devices []hid.DeviceInfo

	if runtime.GOOS == "linux" {
		hub.commsLock.Lock()
		if hub.commsPend > 0 {
			hub.commsLock.Unlock()
			return
		}
	}
	for _, info := range hid.Enumerate(hub.vendorID, 0) {
		for _, id := range hub.productIDs {
			if info.ProductID == id && (info.UsagePage == hub.usageID || info.Interface == hub.endpointID) {
				devices = append(devices, info)
				break
			}
		}
	}
	if runtime.GOOS == "linux" {
		hub.commsLock.Unlock()
	}
	hub.stateLock.Lock()

	wallets := make([]accounts.Wallet, 0, len(devices))
	events := []accounts.WalletEvent{}

	for _, device := range devices {
		url := accounts.URL{Scheme: hub.scheme, Path: device.Path}

		for len(hub.wallets) > 0 {
			_, failure := hub.wallets[0].Status()
			if hub.wallets[0].URL().Cmp(url) >= 0 || failure == nil {
				break
			}
			events = append(events, accounts.WalletEvent{Wallet: hub.wallets[0], Kind: accounts.WalletDropped})
			hub.wallets = hub.wallets[1:]
		}
		if len(hub.wallets) == 0 || hub.wallets[0].URL().Cmp(url) > 0 {
			logger := log4j.New("url", url)
			wallet := &wallet{hub: hub, driver: hub.makeDriver(logger), url: &url, info: device, log: logger}

			events = append(events, accounts.WalletEvent{Wallet: wallet, Kind: accounts.WalletArrived})
			wallets = append(wallets, wallet)
			continue
		}
		if hub.wallets[0].URL().Cmp(url) == 0 {
			wallets = append(wallets, hub.wallets[0])
			hub.wallets = hub.wallets[1:]
			continue
		}
	}
	for _, wallet := range hub.wallets {
		events = append(events, accounts.WalletEvent{Wallet: wallet, Kind: accounts.WalletDropped})
	}
	hub.refreshed = time.Now()
	hub.wallets = wallets
	hub.stateLock.Unlock()

	for _, event := range events {
		hub.updateFeed.Send(event)
	}
}

func (hub *Hub) Subscribe(sink chan<- accounts.WalletEvent) event.Subscription {
	hub.stateLock.Lock()
	defer hub.stateLock.Unlock()

	sub := hub.updateScope.Track(hub.updateFeed.Subscribe(sink))

	if !hub.updating {
		hub.updating = true
		go hub.updater()
	}
	return sub
}

func (hub *Hub) updater() {
	for {
		time.Sleep(refreshCycle)

		hub.refreshWallets()

		hub.stateLock.Lock()
		if hub.updateScope.Count() == 0 {
			hub.updating = false
			hub.stateLock.Unlock()
			return
		}
		hub.stateLock.Unlock()
	}
}
