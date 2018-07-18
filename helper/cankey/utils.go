
package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/5uwifi/canchain/helper/utils"
	"github.com/5uwifi/canchain/repl"
	"github.com/5uwifi/canchain/basis/crypto"
	"gopkg.in/urfave/cli.v1"
)

func promptPassphrase(confirmation bool) string {
	passphrase, err := repl.Stdin.PromptPassword("Passphrase: ")
	if err != nil {
		utils.Fatalf("Failed to read passphrase: %v", err)
	}

	if confirmation {
		confirm, err := repl.Stdin.PromptPassword("Repeat passphrase: ")
		if err != nil {
			utils.Fatalf("Failed to read passphrase confirmation: %v", err)
		}
		if passphrase != confirm {
			utils.Fatalf("Passphrases do not match")
		}
	}

	return passphrase
}

// --passfile command line flag and ultimately prompts the user for a
func getPassphrase(ctx *cli.Context) string {
	// Look for the --passwordfile flag.
	passphraseFile := ctx.String(passphraseFlag.Name)
	if passphraseFile != "" {
		content, err := ioutil.ReadFile(passphraseFile)
		if err != nil {
			utils.Fatalf("Failed to read passphrase file '%s': %v",
				passphraseFile, err)
		}
		return strings.TrimRight(string(content), "\r\n")
	}

	// Otherwise prompt the user for the passphrase.
	return promptPassphrase(false)
}

//
//
func signHash(data []byte) []byte {
	msg := fmt.Sprintf("\x19CANChain Signed Message:\n%d%s", len(data), data)
	return crypto.Keccak256([]byte(msg))
}

func mustPrintJSON(jsonObject interface{}) {
	str, err := json.MarshalIndent(jsonObject, "", "  ")
	if err != nil {
		utils.Fatalf("Failed to marshal JSON object: %v", err)
	}
	fmt.Println(string(str))
}
