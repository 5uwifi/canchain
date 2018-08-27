package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/5uwifi/canchain/cmd/utils"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/rpc"
	"github.com/5uwifi/canchain/signer/core"
	"github.com/5uwifi/canchain/signer/rules"
	"github.com/5uwifi/canchain/signer/storage"
	"gopkg.in/urfave/cli.v1"
)

const ExternalAPIVersion = "2.0.0"

const InternalAPIVersion = "2.0.0"

const legalWarning = `
WARNING! 

Clef is alpha software, and not yet publically released. This software has _not_ been audited, and there
are no guarantees about the workings of this software. It may contain severe flaws. You should not use this software
unless you agree to take full responsibility for doing so, and know what you are doing. 

TLDR; THIS IS NOT PRODUCTION-READY SOFTWARE! 

`

var (
	logLevelFlag = cli.IntFlag{
		Name:  "loglevel",
		Value: 4,
		Usage: "log level to emit to the screen",
	}
	keystoreFlag = cli.StringFlag{
		Name:  "keystore",
		Value: filepath.Join(node.DefaultDataDir(), "keystore"),
		Usage: "Directory for the keystore",
	}
	configdirFlag = cli.StringFlag{
		Name:  "configdir",
		Value: DefaultConfigDir(),
		Usage: "Directory for Clef configuration",
	}
	rpcPortFlag = cli.IntFlag{
		Name:  "rpcport",
		Usage: "HTTP-RPC server listening port",
		Value: node.DefaultHTTPPort + 5,
	}
	signerSecretFlag = cli.StringFlag{
		Name:  "signersecret",
		Usage: "A file containing the password used to encrypt Clef credentials, e.g. keystore credentials and ruleset hash",
	}
	dBFlag = cli.StringFlag{
		Name:  "4bytedb",
		Usage: "File containing 4byte-identifiers",
		Value: "./4byte.json",
	}
	customDBFlag = cli.StringFlag{
		Name:  "4bytedb-custom",
		Usage: "File used for writing new 4byte-identifiers submitted via API",
		Value: "./4byte-custom.json",
	}
	auditLogFlag = cli.StringFlag{
		Name:  "auditlog",
		Usage: "File used to emit audit logs. Set to \"\" to disable",
		Value: "audit.log",
	}
	ruleFlag = cli.StringFlag{
		Name:  "rules",
		Usage: "Enable rule-engine",
		Value: "rules.json",
	}
	stdiouiFlag = cli.BoolFlag{
		Name: "stdio-ui",
		Usage: "Use STDIN/STDOUT as a channel for an external UI. " +
			"This means that an STDIN/STDOUT is used for RPC-communication with a e.g. a graphical user " +
			"interface, and can be used when Clef is started by an external process.",
	}
	testFlag = cli.BoolFlag{
		Name:  "stdio-ui-test",
		Usage: "Mechanism to test interface between Clef and UI. Requires 'stdio-ui'.",
	}
	app         = cli.NewApp()
	initCommand = cli.Command{
		Action:    utils.MigrateFlags(initializeSecrets),
		Name:      "init",
		Usage:     "Initialize the signer, generate secret storage",
		ArgsUsage: "",
		Flags: []cli.Flag{
			logLevelFlag,
			configdirFlag,
		},
		Description: `
The init command generates a master seed which Clef can use to store credentials and data needed for 
the rule-engine to work.`,
	}
	attestCommand = cli.Command{
		Action:    utils.MigrateFlags(attestFile),
		Name:      "attest",
		Usage:     "Attest that a js-file is to be used",
		ArgsUsage: "<sha256sum>",
		Flags: []cli.Flag{
			logLevelFlag,
			configdirFlag,
			signerSecretFlag,
		},
		Description: `
The attest command stores the sha256 of the rule.js-file that you want to use for automatic processing of 
incoming requests. 

Whenever you make an edit to the rule file, you need to use attestation to tell 
Clef that the file is 'safe' to execute.`,
	}

	addCredentialCommand = cli.Command{
		Action:    utils.MigrateFlags(addCredential),
		Name:      "addpw",
		Usage:     "Store a credential for a keystore file",
		ArgsUsage: "<address> <password>",
		Flags: []cli.Flag{
			logLevelFlag,
			configdirFlag,
			signerSecretFlag,
		},
		Description: `
The addpw command stores a password for a given address (keyfile). If you invoke it with only one parameter, it will 
remove any stored credential for that address (keyfile)
`,
	}
)

func init() {
	app.Name = "Clef"
	app.Usage = "Manage CANChain account operations"
	app.Flags = []cli.Flag{
		logLevelFlag,
		keystoreFlag,
		configdirFlag,
		utils.NetworkIdFlag,
		utils.LightKDFFlag,
		utils.NoUSBFlag,
		utils.RPCListenAddrFlag,
		utils.RPCVirtualHostsFlag,
		utils.IPCDisabledFlag,
		utils.IPCPathFlag,
		utils.RPCEnabledFlag,
		rpcPortFlag,
		signerSecretFlag,
		dBFlag,
		customDBFlag,
		auditLogFlag,
		ruleFlag,
		stdiouiFlag,
		testFlag,
	}
	app.Action = signer
	app.Commands = []cli.Command{initCommand, attestCommand, addCredentialCommand}

}
func main() {
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func initializeSecrets(c *cli.Context) error {
	if err := initialize(c); err != nil {
		return err
	}
	configDir := c.String(configdirFlag.Name)

	masterSeed := make([]byte, 256)
	n, err := io.ReadFull(rand.Reader, masterSeed)
	if err != nil {
		return err
	}
	if n != len(masterSeed) {
		return fmt.Errorf("failed to read enough random")
	}
	err = os.Mkdir(configDir, 0700)
	if err != nil && !os.IsExist(err) {
		return err
	}
	location := filepath.Join(configDir, "secrets.dat")
	if _, err := os.Stat(location); err == nil {
		return fmt.Errorf("file %v already exists, will not overwrite", location)
	}
	err = ioutil.WriteFile(location, masterSeed, 0700)
	if err != nil {
		return err
	}
	fmt.Printf("A master seed has been generated into %s\n", location)
	fmt.Printf(`
This is required to be able to store credentials, such as : 
* Passwords for keystores (used by rule engine)
* Storage for javascript rules
* Hash of rule-file

You should treat that file with utmost secrecy, and make a backup of it. 
NOTE: This file does not contain your accounts. Those need to be backed up separately!

`)
	return nil
}
func attestFile(ctx *cli.Context) error {
	if len(ctx.Args()) < 1 {
		utils.Fatalf("This command requires an argument.")
	}
	if err := initialize(ctx); err != nil {
		return err
	}

	stretchedKey, err := readMasterKey(ctx)
	if err != nil {
		utils.Fatalf(err.Error())
	}
	configDir := ctx.String(configdirFlag.Name)
	vaultLocation := filepath.Join(configDir, common.Bytes2Hex(crypto.Keccak256([]byte("vault"), stretchedKey)[:10]))
	confKey := crypto.Keccak256([]byte("config"), stretchedKey)

	configStorage := storage.NewAESEncryptedStorage(filepath.Join(vaultLocation, "config.json"), confKey)
	val := ctx.Args().First()
	configStorage.Put("ruleset_sha256", val)
	log4j.Info("Ruleset attestation updated", "sha256", val)
	return nil
}

func addCredential(ctx *cli.Context) error {
	if len(ctx.Args()) < 1 {
		utils.Fatalf("This command requires at leaste one argument.")
	}
	if err := initialize(ctx); err != nil {
		return err
	}

	stretchedKey, err := readMasterKey(ctx)
	if err != nil {
		utils.Fatalf(err.Error())
	}
	configDir := ctx.String(configdirFlag.Name)
	vaultLocation := filepath.Join(configDir, common.Bytes2Hex(crypto.Keccak256([]byte("vault"), stretchedKey)[:10]))
	pwkey := crypto.Keccak256([]byte("credentials"), stretchedKey)

	pwStorage := storage.NewAESEncryptedStorage(filepath.Join(vaultLocation, "credentials.json"), pwkey)
	key := ctx.Args().First()
	value := ""
	if len(ctx.Args()) > 1 {
		value = ctx.Args().Get(1)
	}
	pwStorage.Put(key, value)
	log4j.Info("Credential store updated", "key", key)
	return nil
}

func initialize(c *cli.Context) error {
	logOutput := os.Stdout
	if c.Bool(stdiouiFlag.Name) {
		logOutput = os.Stderr
		fmt.Fprintf(logOutput, legalWarning)
	} else {
		if !confirm(legalWarning) {
			return fmt.Errorf("aborted by user")
		}
	}

	log4j.Root().SetHandler(log4j.LvlFilterHandler(log4j.Lvl(c.Int(logLevelFlag.Name)), log4j.StreamHandler(logOutput, log4j.TerminalFormat(true))))
	return nil
}

func signer(c *cli.Context) error {
	if err := initialize(c); err != nil {
		return err
	}
	var (
		ui core.SignerUI
	)
	if c.Bool(stdiouiFlag.Name) {
		log4j.Info("Using stdin/stdout as UI-channel")
		ui = core.NewStdIOUI()
	} else {
		log4j.Info("Using CLI as UI-channel")
		ui = core.NewCommandlineUI()
	}
	db, err := core.NewAbiDBFromFiles(c.String(dBFlag.Name), c.String(customDBFlag.Name))
	if err != nil {
		utils.Fatalf(err.Error())
	}
	log4j.Info("Loaded 4byte db", "signatures", db.Size(), "file", c.String("4bytedb"))

	var (
		api core.ExternalAPI
	)

	configDir := c.String(configdirFlag.Name)
	if stretchedKey, err := readMasterKey(c); err != nil {
		log4j.Info("No master seed provided, rules disabled")
	} else {

		if err != nil {
			utils.Fatalf(err.Error())
		}
		vaultLocation := filepath.Join(configDir, common.Bytes2Hex(crypto.Keccak256([]byte("vault"), stretchedKey)[:10]))

		pwkey := crypto.Keccak256([]byte("credentials"), stretchedKey)
		jskey := crypto.Keccak256([]byte("jsstorage"), stretchedKey)
		confkey := crypto.Keccak256([]byte("config"), stretchedKey)

		pwStorage := storage.NewAESEncryptedStorage(filepath.Join(vaultLocation, "credentials.json"), pwkey)
		jsStorage := storage.NewAESEncryptedStorage(filepath.Join(vaultLocation, "jsstorage.json"), jskey)
		configStorage := storage.NewAESEncryptedStorage(filepath.Join(vaultLocation, "config.json"), confkey)

		ruleJS, err := ioutil.ReadFile(c.String(ruleFlag.Name))
		if err != nil {
			log4j.Info("Could not load rulefile, rules not enabled", "file", "rulefile")
		} else {
			hasher := sha256.New()
			hasher.Write(ruleJS)
			shasum := hasher.Sum(nil)
			storedShasum := configStorage.Get("ruleset_sha256")
			if storedShasum != hex.EncodeToString(shasum) {
				log4j.Info("Could not validate ruleset hash, rules not enabled", "got", hex.EncodeToString(shasum), "expected", storedShasum)
			} else {
				ruleEngine, err := rules.NewRuleEvaluator(ui, jsStorage, pwStorage)
				if err != nil {
					utils.Fatalf(err.Error())
				}
				ruleEngine.Init(string(ruleJS))
				ui = ruleEngine
				log4j.Info("Rule engine configured", "file", c.String(ruleFlag.Name))
			}
		}
	}

	apiImpl := core.NewSignerAPI(
		c.Int64(utils.NetworkIdFlag.Name),
		c.String(keystoreFlag.Name),
		c.Bool(utils.NoUSBFlag.Name),
		ui, db,
		c.Bool(utils.LightKDFFlag.Name))

	api = apiImpl

	if logfile := c.String(auditLogFlag.Name); logfile != "" {
		api, err = core.NewAuditLogger(logfile, api)
		if err != nil {
			utils.Fatalf(err.Error())
		}
		log4j.Info("Audit logs configured", "file", logfile)
	}
	var (
		extapiURL = "n/a"
		ipcapiURL = "n/a"
	)
	rpcAPI := []rpc.API{
		{
			Namespace: "account",
			Public:    true,
			Service:   api,
			Version:   "1.0"},
	}
	if c.Bool(utils.RPCEnabledFlag.Name) {

		vhosts := splitAndTrim(c.GlobalString(utils.RPCVirtualHostsFlag.Name))
		cors := splitAndTrim(c.GlobalString(utils.RPCCORSDomainFlag.Name))

		httpEndpoint := fmt.Sprintf("%s:%d", c.String(utils.RPCListenAddrFlag.Name), c.Int(rpcPortFlag.Name))
		listener, _, err := rpc.StartHTTPEndpoint(httpEndpoint, rpcAPI, []string{"account"}, cors, vhosts, rpc.DefaultHTTPTimeouts)
		if err != nil {
			utils.Fatalf("Could not start RPC api: %v", err)
		}
		extapiURL = fmt.Sprintf("http://%s", httpEndpoint)
		log4j.Info("HTTP endpoint opened", "url", extapiURL)

		defer func() {
			listener.Close()
			log4j.Info("HTTP endpoint closed", "url", httpEndpoint)
		}()

	}
	if !c.Bool(utils.IPCDisabledFlag.Name) {
		if c.IsSet(utils.IPCPathFlag.Name) {
			ipcapiURL = c.String(utils.IPCPathFlag.Name)
		} else {
			ipcapiURL = filepath.Join(configDir, "clef.ipc")
		}

		listener, _, err := rpc.StartIPCEndpoint(ipcapiURL, rpcAPI)
		if err != nil {
			utils.Fatalf("Could not start IPC api: %v", err)
		}
		log4j.Info("IPC endpoint opened", "url", ipcapiURL)
		defer func() {
			listener.Close()
			log4j.Info("IPC endpoint closed", "url", ipcapiURL)
		}()

	}

	if c.Bool(testFlag.Name) {
		log4j.Info("Performing UI test")
		go testExternalUI(apiImpl)
	}
	ui.OnSignerStartup(core.StartupInfo{
		Info: map[string]interface{}{
			"extapi_version": ExternalAPIVersion,
			"intapi_version": InternalAPIVersion,
			"extapi_http":    extapiURL,
			"extapi_ipc":     ipcapiURL,
		},
	})

	abortChan := make(chan os.Signal)
	signal.Notify(abortChan, os.Interrupt)

	sig := <-abortChan
	log4j.Info("Exiting...", "signal", sig)

	return nil
}

func splitAndTrim(input string) []string {
	result := strings.Split(input, ",")
	for i, r := range result {
		result[i] = strings.TrimSpace(r)
	}
	return result
}

func DefaultConfigDir() string {
	home := homeDir()
	if home != "" {
		if runtime.GOOS == "darwin" {
			return filepath.Join(home, "Library", "Signer")
		} else if runtime.GOOS == "windows" {
			return filepath.Join(home, "AppData", "Roaming", "Signer")
		} else {
			return filepath.Join(home, ".clef")
		}
	}
	return ""
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if usr, err := user.Current(); err == nil {
		return usr.HomeDir
	}
	return ""
}
func readMasterKey(ctx *cli.Context) ([]byte, error) {
	var (
		file      string
		configDir = ctx.String(configdirFlag.Name)
	)
	if ctx.IsSet(signerSecretFlag.Name) {
		file = ctx.String(signerSecretFlag.Name)
	} else {
		file = filepath.Join(configDir, "secrets.dat")
	}
	if err := checkFile(file); err != nil {
		return nil, err
	}
	masterKey, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if len(masterKey) < 256 {
		return nil, fmt.Errorf("master key of insufficient length, expected >255 bytes, got %d", len(masterKey))
	}
	vaultLocation := filepath.Join(configDir, common.Bytes2Hex(crypto.Keccak256([]byte("vault"), masterKey)[:10]))
	err = os.Mkdir(vaultLocation, 0700)
	if err != nil && !os.IsExist(err) {
		return nil, err
	}

	return masterKey, nil
}

func checkFile(filename string) error {
	info, err := os.Stat(filename)
	if err != nil {
		return fmt.Errorf("failed stat on %s: %v", filename, err)
	}
	if info.Mode().Perm()&077 != 0 {
		return fmt.Errorf("file (%v) has insecure file permissions (%v)", filename, info.Mode().String())
	}
	return nil
}

func confirm(text string) bool {
	fmt.Printf(text)
	fmt.Printf("\nEnter 'ok' to proceed:\n>")

	text, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		log4j.Crit("Failed to read user input", "err", err)
	}

	if text := strings.TrimSpace(text); text == "ok" {
		return true
	}
	return false
}

func testExternalUI(api *core.SignerAPI) {

	ctx := context.WithValue(context.Background(), "remote", "clef binary")
	ctx = context.WithValue(ctx, "scheme", "in-proc")
	ctx = context.WithValue(ctx, "local", "main")

	errs := make([]string, 0)

	api.UI.ShowInfo("Testing 'ShowInfo'")
	api.UI.ShowError("Testing 'ShowError'")

	checkErr := func(method string, err error) {
		if err != nil && err != core.ErrRequestDenied {
			errs = append(errs, fmt.Sprintf("%v: %v", method, err.Error()))
		}
	}
	var err error

	_, err = api.SignTransaction(ctx, core.SendTxArgs{From: common.MixedcaseAddress{}}, nil)
	checkErr("SignTransaction", err)
	_, err = api.Sign(ctx, common.MixedcaseAddress{}, common.Hex2Bytes("01020304"))
	checkErr("Sign", err)
	_, err = api.List(ctx)
	checkErr("List", err)
	_, err = api.New(ctx)
	checkErr("New", err)
	_, err = api.Export(ctx, common.Address{})
	checkErr("Export", err)
	_, err = api.Import(ctx, json.RawMessage{})
	checkErr("Import", err)

	api.UI.ShowInfo("Tests completed")

	if len(errs) > 0 {
		log4j.Error("Got errors")
		for _, e := range errs {
			log4j.Error(e)
		}
	} else {
		log4j.Info("No errors")
	}

}

/**

curl -H "Content-Type: application/json" -X POST --data '{"jsonrpc":"2.0","method":"account_new","params":["test"],"id":67}' localhost:8550


curl -i -H "Content-Type: application/json" -X POST --data '{"jsonrpc":"2.0","method":"account_list","params":[""],"id":67}' http://localhost:8550/


curl -i -H "Content-Type: application/json" -X POST --data '{"jsonrpc":"2.0","method":"account_signTransaction","params":[{"from":"0x82A2A876D39022B3019932D30Cd9c97ad5616813","gas":"0x333","gasPrice":"0x123","nonce":"0x0","to":"0x07a565b7ed7d7a678680a4c162885bedbb695fe0", "value":"0x10", "data":"0x4401a6e40000000000000000000000000000000000000000000000000000000000000012"},"test"],"id":67}' http://localhost:8550/

curl -i -H "Content-Type: application/json" -X POST --data '{"jsonrpc":"2.0","method":"account_signTransaction","params":[{"from":"0x82A2A876D39022B3019932D30Cd9c97ad5616813","gas":"0x333","gasPrice":"0x123","nonce":"0x0","to":"0x07a565b7ed7d7a678680a4c162885bedbb695fe0", "value":"0x10", "data":"0x4401a6e40000000000000000000000000000000000000000000000000000000000000012"}],"id":67}' http://localhost:8550/


curl -i -H "Content-Type: application/json" -X POST --data '{"jsonrpc":"2.0","method":"account_sign","params":["0x694267f14675d7e1b9494fd8d72fefe1755710fa","bazonk gaz baz"],"id":67}' http://localhost:8550/


**/
