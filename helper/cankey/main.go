//
// (at your option) any later version.
//
//

package main

import (
	"fmt"
	"os"

	"github.com/5uwifi/canchain/helper/utils"
	"gopkg.in/urfave/cli.v1"
)

const (
	defaultKeyfileName = "keyfile.json"
)

var gitCommit = ""

var app *cli.App

func init() {
	app = utils.NewApp(gitCommit, "an Ethereum key manager")
	app.Commands = []cli.Command{
		commandGenerate,
		commandInspect,
		commandChangePassphrase,
		commandSignMessage,
		commandVerifyMessage,
	}
}

var (
	passphraseFlag = cli.StringFlag{
		Name:  "passwordfile",
		Usage: "the file that contains the passphrase for the keyfile",
	}
	jsonFlag = cli.BoolFlag{
		Name:  "json",
		Usage: "output JSON instead of human-readable format",
	}
)

func main() {
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
