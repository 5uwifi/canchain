package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math/big"
	"reflect"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/accounts/keystore"
	"github.com/5uwifi/canchain/accounts/usbwallet"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/privacy/canapi"
)

const numberOfAccountsToDerive = 10

type ExternalAPI interface {
	List(ctx context.Context) ([]common.Address, error)
	New(ctx context.Context) (accounts.Account, error)
	SignTransaction(ctx context.Context, args SendTxArgs, methodSelector *string) (*canapi.SignTransactionResult, error)
	Sign(ctx context.Context, addr common.MixedcaseAddress, data hexutil.Bytes) (hexutil.Bytes, error)
	Export(ctx context.Context, addr common.Address) (json.RawMessage, error)
}

type SignerUI interface {
	ApproveTx(request *SignTxRequest) (SignTxResponse, error)
	ApproveSignData(request *SignDataRequest) (SignDataResponse, error)
	ApproveExport(request *ExportRequest) (ExportResponse, error)
	ApproveImport(request *ImportRequest) (ImportResponse, error)
	ApproveListing(request *ListRequest) (ListResponse, error)
	ApproveNewAccount(request *NewAccountRequest) (NewAccountResponse, error)
	ShowError(message string)
	ShowInfo(message string)
	OnApprovedTx(tx canapi.SignTransactionResult)
	OnSignerStartup(info StartupInfo)
	OnInputRequired(info UserInputRequest) (UserInputResponse, error)
}

type SignerAPI struct {
	chainID    *big.Int
	am         *accounts.Manager
	UI         SignerUI
	validator  *Validator
	rejectMode bool
}

type Metadata struct {
	Remote    string `json:"remote"`
	Local     string `json:"local"`
	Scheme    string `json:"scheme"`
	UserAgent string `json:"User-Agent"`
	Origin    string `json:"Origin"`
}

func MetadataFromContext(ctx context.Context) Metadata {
	m := Metadata{"NA", "NA", "NA", "", ""}

	if v := ctx.Value("remote"); v != nil {
		m.Remote = v.(string)
	}
	if v := ctx.Value("scheme"); v != nil {
		m.Scheme = v.(string)
	}
	if v := ctx.Value("local"); v != nil {
		m.Local = v.(string)
	}
	if v := ctx.Value("Origin"); v != nil {
		m.Origin = v.(string)
	}
	if v := ctx.Value("User-Agent"); v != nil {
		m.UserAgent = v.(string)
	}
	return m
}

func (m Metadata) String() string {
	s, err := json.Marshal(m)
	if err == nil {
		return string(s)
	}
	return err.Error()
}

type (
	SignTxRequest struct {
		Transaction SendTxArgs       `json:"transaction"`
		Callinfo    []ValidationInfo `json:"call_info"`
		Meta        Metadata         `json:"meta"`
	}
	SignTxResponse struct {
		Transaction SendTxArgs `json:"transaction"`
		Approved    bool       `json:"approved"`
		Password    string     `json:"password"`
	}
	ExportRequest struct {
		Address common.Address `json:"address"`
		Meta    Metadata       `json:"meta"`
	}
	ExportResponse struct {
		Approved bool `json:"approved"`
	}
	ImportRequest struct {
		Meta Metadata `json:"meta"`
	}
	ImportResponse struct {
		Approved    bool   `json:"approved"`
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	SignDataRequest struct {
		Address common.MixedcaseAddress `json:"address"`
		Rawdata hexutil.Bytes           `json:"raw_data"`
		Message string                  `json:"message"`
		Hash    hexutil.Bytes           `json:"hash"`
		Meta    Metadata                `json:"meta"`
	}
	SignDataResponse struct {
		Approved bool `json:"approved"`
		Password string
	}
	NewAccountRequest struct {
		Meta Metadata `json:"meta"`
	}
	NewAccountResponse struct {
		Approved bool   `json:"approved"`
		Password string `json:"password"`
	}
	ListRequest struct {
		Accounts []Account `json:"accounts"`
		Meta     Metadata  `json:"meta"`
	}
	ListResponse struct {
		Accounts []Account `json:"accounts"`
	}
	Message struct {
		Text string `json:"text"`
	}
	StartupInfo struct {
		Info map[string]interface{} `json:"info"`
	}
	UserInputRequest struct {
		Prompt     string `json:"prompt"`
		Title      string `json:"title"`
		IsPassword bool   `json:"isPassword"`
	}
	UserInputResponse struct {
		Text string `json:"text"`
	}
)

var ErrRequestDenied = errors.New("Request denied")

func NewSignerAPI(chainID int64, ksLocation string, noUSB bool, ui SignerUI, abidb *AbiDb, lightKDF bool, advancedMode bool) *SignerAPI {
	var (
		backends []accounts.Backend
		n, p     = keystore.StandardScryptN, keystore.StandardScryptP
	)
	if lightKDF {
		n, p = keystore.LightScryptN, keystore.LightScryptP
	}
	if len(ksLocation) > 0 {
		backends = append(backends, keystore.NewKeyStore(ksLocation, n, p))
	}
	if advancedMode {
		log4j.Info("Clef is in advanced mode: will warn instead of reject")
	}
	if !noUSB {
		if ledgerhub, err := usbwallet.NewLedgerHub(); err != nil {
			log4j.Warn(fmt.Sprintf("Failed to start Ledger hub, disabling: %v", err))
		} else {
			backends = append(backends, ledgerhub)
			log4j.Debug("Ledger support enabled")
		}
		if trezorhub, err := usbwallet.NewTrezorHub(); err != nil {
			log4j.Warn(fmt.Sprintf("Failed to start Trezor hub, disabling: %v", err))
		} else {
			backends = append(backends, trezorhub)
			log4j.Debug("Trezor support enabled")
		}
	}
	signer := &SignerAPI{big.NewInt(chainID), accounts.NewManager(backends...), ui, NewValidator(abidb), !advancedMode}
	if !noUSB {
		signer.startUSBListener()
	}
	return signer
}
func (api *SignerAPI) openTrezor(url accounts.URL) {
	resp, err := api.UI.OnInputRequired(UserInputRequest{
		Prompt: "Pin required to open Trezor wallet\n" +
			"Look at the device for number positions\n\n" +
			"7 | 8 | 9\n" +
			"--+---+--\n" +
			"4 | 5 | 6\n" +
			"--+---+--\n" +
			"1 | 2 | 3\n\n",
		IsPassword: true,
		Title:      "Trezor unlock",
	})
	if err != nil {
		log4j.Warn("failed getting trezor pin", "err", err)
		return
	}
	w, err := api.am.Wallet(url.String())
	if err != nil {
		log4j.Warn("wallet unavailable", "url", url)
		return
	}
	err = w.Open(resp.Text)
	if err != nil {
		log4j.Warn("failed to open wallet", "wallet", url, "err", err)
		return
	}

}

func (api *SignerAPI) startUSBListener() {
	events := make(chan accounts.WalletEvent, 16)
	am := api.am
	am.Subscribe(events)
	go func() {

		for _, wallet := range am.Wallets() {
			if err := wallet.Open(""); err != nil {
				log4j.Warn("Failed to open wallet", "url", wallet.URL(), "err", err)
				if err == usbwallet.ErrTrezorPINNeeded {
					go api.openTrezor(wallet.URL())
				}
			}
		}
		for event := range events {
			switch event.Kind {
			case accounts.WalletArrived:
				if err := event.Wallet.Open(""); err != nil {
					log4j.Warn("New wallet appeared, failed to open", "url", event.Wallet.URL(), "err", err)
					if err == usbwallet.ErrTrezorPINNeeded {
						go api.openTrezor(event.Wallet.URL())
					}
				}
			case accounts.WalletOpened:
				status, _ := event.Wallet.Status()
				log4j.Info("New wallet appeared", "url", event.Wallet.URL(), "status", status)

				derivationPath := accounts.DefaultBaseDerivationPath
				if event.Wallet.URL().Scheme == "ledger" {
					derivationPath = accounts.DefaultLedgerBaseDerivationPath
				}
				var nextPath = derivationPath
				for i := 0; i < numberOfAccountsToDerive; i++ {
					acc, err := event.Wallet.Derive(nextPath, true)
					if err != nil {
						log4j.Warn("account derivation failed", "error", err)
					} else {
						log4j.Info("derived account", "address", acc.Address)
					}
					nextPath[len(nextPath)-1]++
				}
			case accounts.WalletDropped:
				log4j.Info("Old wallet dropped", "url", event.Wallet.URL())
				event.Wallet.Close()
			}
		}
	}()
}

func (api *SignerAPI) List(ctx context.Context) ([]common.Address, error) {
	var accs []Account
	for _, wallet := range api.am.Wallets() {
		for _, acc := range wallet.Accounts() {
			acc := Account{Typ: "Account", URL: wallet.URL(), Address: acc.Address}
			accs = append(accs, acc)
		}
	}
	result, err := api.UI.ApproveListing(&ListRequest{Accounts: accs, Meta: MetadataFromContext(ctx)})
	if err != nil {
		return nil, err
	}
	if result.Accounts == nil {
		return nil, ErrRequestDenied

	}

	addresses := make([]common.Address, 0)
	for _, acc := range result.Accounts {
		addresses = append(addresses, acc.Address)
	}

	return addresses, nil
}

func (api *SignerAPI) New(ctx context.Context) (accounts.Account, error) {
	be := api.am.Backends(keystore.KeyStoreType)
	if len(be) == 0 {
		return accounts.Account{}, errors.New("password based accounts not supported")
	}
	var (
		resp NewAccountResponse
		err  error
	)
	for i := 0; i < 3; i++ {
		resp, err = api.UI.ApproveNewAccount(&NewAccountRequest{MetadataFromContext(ctx)})
		if err != nil {
			return accounts.Account{}, err
		}
		if !resp.Approved {
			return accounts.Account{}, ErrRequestDenied
		}
		if pwErr := ValidatePasswordFormat(resp.Password); pwErr != nil {
			api.UI.ShowError(fmt.Sprintf("Account creation attempt #%d failed due to password requirements: %v", (i + 1), pwErr))
		} else {
			return be[0].(*keystore.KeyStore).NewAccount(resp.Password)
		}
	}
	return accounts.Account{}, errors.New("account creation failed")
}

func logDiff(original *SignTxRequest, new *SignTxResponse) bool {
	modified := false
	if f0, f1 := original.Transaction.From, new.Transaction.From; !reflect.DeepEqual(f0, f1) {
		log4j.Info("Sender-account changed by UI", "was", f0, "is", f1)
		modified = true
	}
	if t0, t1 := original.Transaction.To, new.Transaction.To; !reflect.DeepEqual(t0, t1) {
		log4j.Info("Recipient-account changed by UI", "was", t0, "is", t1)
		modified = true
	}
	if g0, g1 := original.Transaction.Gas, new.Transaction.Gas; g0 != g1 {
		modified = true
		log4j.Info("Gas changed by UI", "was", g0, "is", g1)
	}
	if g0, g1 := big.Int(original.Transaction.GasPrice), big.Int(new.Transaction.GasPrice); g0.Cmp(&g1) != 0 {
		modified = true
		log4j.Info("GasPrice changed by UI", "was", g0, "is", g1)
	}
	if v0, v1 := big.Int(original.Transaction.Value), big.Int(new.Transaction.Value); v0.Cmp(&v1) != 0 {
		modified = true
		log4j.Info("Value changed by UI", "was", v0, "is", v1)
	}
	if d0, d1 := original.Transaction.Data, new.Transaction.Data; d0 != d1 {
		d0s := ""
		d1s := ""
		if d0 != nil {
			d0s = hexutil.Encode(*d0)
		}
		if d1 != nil {
			d1s = hexutil.Encode(*d1)
		}
		if d1s != d0s {
			modified = true
			log4j.Info("Data changed by UI", "was", d0s, "is", d1s)
		}
	}
	if n0, n1 := original.Transaction.Nonce, new.Transaction.Nonce; n0 != n1 {
		modified = true
		log4j.Info("Nonce changed by UI", "was", n0, "is", n1)
	}
	return modified
}

func (api *SignerAPI) SignTransaction(ctx context.Context, args SendTxArgs, methodSelector *string) (*canapi.SignTransactionResult, error) {
	var (
		err    error
		result SignTxResponse
	)
	msgs, err := api.validator.ValidateTransaction(&args, methodSelector)
	if err != nil {
		return nil, err
	}
	if api.rejectMode {
		if err := msgs.getWarnings(); err != nil {
			return nil, err
		}
	}

	req := SignTxRequest{
		Transaction: args,
		Meta:        MetadataFromContext(ctx),
		Callinfo:    msgs.Messages,
	}
	result, err = api.UI.ApproveTx(&req)
	if err != nil {
		return nil, err
	}
	if !result.Approved {
		return nil, ErrRequestDenied
	}
	logDiff(&req, &result)
	var (
		acc    accounts.Account
		wallet accounts.Wallet
	)
	acc = accounts.Account{Address: result.Transaction.From.Address()}
	wallet, err = api.am.Find(acc)
	if err != nil {
		return nil, err
	}
	var unsignedTx = result.Transaction.toTransaction()

	signedTx, err := wallet.SignTxWithPassphrase(acc, result.Password, unsignedTx, api.chainID)
	if err != nil {
		api.UI.ShowError(err.Error())
		return nil, err
	}

	rlpdata, err := rlp.EncodeToBytes(signedTx)
	response := canapi.SignTransactionResult{Raw: rlpdata, Tx: signedTx}

	api.UI.OnApprovedTx(response)
	return &response, nil

}

func (api *SignerAPI) Sign(ctx context.Context, addr common.MixedcaseAddress, data hexutil.Bytes) (hexutil.Bytes, error) {
	sighash, msg := SignHash(data)
	req := &SignDataRequest{Address: addr, Rawdata: data, Message: msg, Hash: sighash, Meta: MetadataFromContext(ctx)}
	res, err := api.UI.ApproveSignData(req)

	if err != nil {
		return nil, err
	}
	if !res.Approved {
		return nil, ErrRequestDenied
	}
	account := accounts.Account{Address: addr.Address()}
	wallet, err := api.am.Find(account)
	if err != nil {
		return nil, err
	}
	signature, err := wallet.SignHashWithPassphrase(account, res.Password, sighash)
	if err != nil {
		api.UI.ShowError(err.Error())
		return nil, err
	}
	signature[64] += 27
	return signature, nil
}

func SignHash(data []byte) ([]byte, string) {
	msg := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(data), data)
	return crypto.Keccak256([]byte(msg)), msg
}

func (api *SignerAPI) Export(ctx context.Context, addr common.Address) (json.RawMessage, error) {
	res, err := api.UI.ApproveExport(&ExportRequest{Address: addr, Meta: MetadataFromContext(ctx)})

	if err != nil {
		return nil, err
	}
	if !res.Approved {
		return nil, ErrRequestDenied
	}
	wallet, err := api.am.Find(accounts.Account{Address: addr})
	if err != nil {
		return nil, err
	}
	if wallet.URL().Scheme != keystore.KeyStoreScheme {
		return nil, fmt.Errorf("Account is not a keystore-account")
	}
	return ioutil.ReadFile(wallet.URL().Path)
}

func (api *SignerAPI) Import(ctx context.Context, keyJSON json.RawMessage) (Account, error) {
	be := api.am.Backends(keystore.KeyStoreType)

	if len(be) == 0 {
		return Account{}, errors.New("password based accounts not supported")
	}
	res, err := api.UI.ApproveImport(&ImportRequest{Meta: MetadataFromContext(ctx)})

	if err != nil {
		return Account{}, err
	}
	if !res.Approved {
		return Account{}, ErrRequestDenied
	}
	acc, err := be[0].(*keystore.KeyStore).Import(keyJSON, res.OldPassword, res.NewPassword)
	if err != nil {
		api.UI.ShowError(err.Error())
		return Account{}, err
	}
	return Account{Typ: "Account", URL: acc.URL, Address: acc.Address}, nil
}
