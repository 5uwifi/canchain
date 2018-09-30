package core

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"sync"

	"github.com/davecgh/go-spew/spew"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/privacy/canapi"
	"golang.org/x/crypto/ssh/terminal"
)

type CommandlineUI struct {
	in *bufio.Reader
	mu sync.Mutex
}

func NewCommandlineUI() *CommandlineUI {
	return &CommandlineUI{in: bufio.NewReader(os.Stdin)}
}

func (ui *CommandlineUI) readString() string {
	for {
		fmt.Printf("> ")
		text, err := ui.in.ReadString('\n')
		if err != nil {
			log4j.Crit("Failed to read user input", "err", err)
		}
		if text = strings.TrimSpace(text); text != "" {
			return text
		}
	}
}

func (ui *CommandlineUI) readPassword() string {
	fmt.Printf("Enter password to approve:\n")
	fmt.Printf("> ")

	text, err := terminal.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		log4j.Crit("Failed to read password", "err", err)
	}
	fmt.Println()
	fmt.Println("-----------------------")
	return string(text)
}

func (ui *CommandlineUI) readPasswordText(inputstring string) string {
	fmt.Printf("Enter %s:\n", inputstring)
	fmt.Printf("> ")
	text, err := terminal.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		log4j.Crit("Failed to read password", "err", err)
	}
	fmt.Println("-----------------------")
	return string(text)
}

func (ui *CommandlineUI) OnInputRequired(info UserInputRequest) (UserInputResponse, error) {
	fmt.Println(info.Title)
	fmt.Println(info.Prompt)
	if info.IsPassword {
		text, err := terminal.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			log4j.Error("Failed to read password", "err", err)
		}
		fmt.Println("-----------------------")
		return UserInputResponse{string(text)}, err
	}
	text := ui.readString()
	fmt.Println("-----------------------")
	return UserInputResponse{text}, nil
}

func (ui *CommandlineUI) confirm() bool {
	fmt.Printf("Approve? [y/N]:\n")
	if ui.readString() == "y" {
		return true
	}
	fmt.Println("-----------------------")
	return false
}

func showMetadata(metadata Metadata) {
	fmt.Printf("Request context:\n\t%v -> %v -> %v\n", metadata.Remote, metadata.Scheme, metadata.Local)
	fmt.Printf("\nAdditional HTTP header data, provided by the external caller:\n")
	fmt.Printf("\tUser-Agent: %v\n\tOrigin: %v\n", metadata.UserAgent, metadata.Origin)
}

func (ui *CommandlineUI) ApproveTx(request *SignTxRequest) (SignTxResponse, error) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	weival := request.Transaction.Value.ToInt()
	fmt.Printf("--------- Transaction request-------------\n")
	if to := request.Transaction.To; to != nil {
		fmt.Printf("to:    %v\n", to.Original())
		if !to.ValidChecksum() {
			fmt.Printf("\nWARNING: Invalid checksum on to-address!\n\n")
		}
	} else {
		fmt.Printf("to:    <contact creation>\n")
	}
	fmt.Printf("from:     %v\n", request.Transaction.From.String())
	fmt.Printf("value:    %v wei\n", weival)
	fmt.Printf("gas:      %v (%v)\n", request.Transaction.Gas, uint64(request.Transaction.Gas))
	fmt.Printf("gasprice: %v wei\n", request.Transaction.GasPrice.ToInt())
	fmt.Printf("nonce:    %v (%v)\n", request.Transaction.Nonce, uint64(request.Transaction.Nonce))
	if request.Transaction.Data != nil {
		d := *request.Transaction.Data
		if len(d) > 0 {

			fmt.Printf("data:     %v\n", hexutil.Encode(d))
		}
	}
	if request.Callinfo != nil {
		fmt.Printf("\nTransaction validation:\n")
		for _, m := range request.Callinfo {
			fmt.Printf("  * %s : %s\n", m.Typ, m.Message)
		}
		fmt.Println()

	}
	fmt.Printf("\n")
	showMetadata(request.Meta)
	fmt.Printf("-------------------------------------------\n")
	if !ui.confirm() {
		return SignTxResponse{request.Transaction, false, ""}, nil
	}
	return SignTxResponse{request.Transaction, true, ui.readPassword()}, nil
}

func (ui *CommandlineUI) ApproveSignData(request *SignDataRequest) (SignDataResponse, error) {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	fmt.Printf("-------- Sign data request--------------\n")
	fmt.Printf("Account:  %s\n", request.Address.String())
	fmt.Printf("message:  \n%q\n", request.Message)
	fmt.Printf("raw data: \n%v\n", request.Rawdata)
	fmt.Printf("message hash:  %v\n", request.Hash)
	fmt.Printf("-------------------------------------------\n")
	showMetadata(request.Meta)
	if !ui.confirm() {
		return SignDataResponse{false, ""}, nil
	}
	return SignDataResponse{true, ui.readPassword()}, nil
}

func (ui *CommandlineUI) ApproveExport(request *ExportRequest) (ExportResponse, error) {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	fmt.Printf("-------- Export Account request--------------\n")
	fmt.Printf("A request has been made to export the (encrypted) keyfile\n")
	fmt.Printf("Approving this operation means that the caller obtains the (encrypted) contents\n")
	fmt.Printf("\n")
	fmt.Printf("Account:  %x\n", request.Address)
	fmt.Printf("-------------------------------------------\n")
	showMetadata(request.Meta)
	return ExportResponse{ui.confirm()}, nil
}

func (ui *CommandlineUI) ApproveImport(request *ImportRequest) (ImportResponse, error) {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	fmt.Printf("-------- Import Account request--------------\n")
	fmt.Printf("A request has been made to import an encrypted keyfile\n")
	fmt.Printf("-------------------------------------------\n")
	showMetadata(request.Meta)
	if !ui.confirm() {
		return ImportResponse{false, "", ""}, nil
	}
	return ImportResponse{true, ui.readPasswordText("Old password"), ui.readPasswordText("New password")}, nil
}

func (ui *CommandlineUI) ApproveListing(request *ListRequest) (ListResponse, error) {

	ui.mu.Lock()
	defer ui.mu.Unlock()

	fmt.Printf("-------- List Account request--------------\n")
	fmt.Printf("A request has been made to list all accounts. \n")
	fmt.Printf("You can select which accounts the caller can see\n")
	for _, account := range request.Accounts {
		fmt.Printf("  [x] %v\n", account.Address.Hex())
		fmt.Printf("    URL: %v\n", account.URL)
		fmt.Printf("    Type: %v\n", account.Typ)
	}
	fmt.Printf("-------------------------------------------\n")
	showMetadata(request.Meta)
	if !ui.confirm() {
		return ListResponse{nil}, nil
	}
	return ListResponse{request.Accounts}, nil
}

func (ui *CommandlineUI) ApproveNewAccount(request *NewAccountRequest) (NewAccountResponse, error) {

	ui.mu.Lock()
	defer ui.mu.Unlock()

	fmt.Printf("-------- New Account request--------------\n\n")
	fmt.Printf("A request has been made to create a new account. \n")
	fmt.Printf("Approving this operation means that a new account is created,\n")
	fmt.Printf("and the address is returned to the external caller\n\n")
	showMetadata(request.Meta)
	if !ui.confirm() {
		return NewAccountResponse{false, ""}, nil
	}
	return NewAccountResponse{true, ui.readPassword()}, nil
}

func (ui *CommandlineUI) ShowError(message string) {
	fmt.Printf("-------- Error message from Clef-----------\n")
	fmt.Println(message)
	fmt.Printf("-------------------------------------------\n")
}

func (ui *CommandlineUI) ShowInfo(message string) {
	fmt.Printf("Info: %v\n", message)
}

func (ui *CommandlineUI) OnApprovedTx(tx canapi.SignTransactionResult) {
	fmt.Printf("Transaction signed:\n ")
	spew.Dump(tx.Tx)
}

func (ui *CommandlineUI) OnSignerStartup(info StartupInfo) {

	fmt.Printf("------- Signer info -------\n")
	for k, v := range info.Info {
		fmt.Printf("* %v : %v\n", k, v)
	}
}
