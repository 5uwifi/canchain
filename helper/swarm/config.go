package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	cli "gopkg.in/urfave/cli.v1"

	"github.com/5uwifi/canchain/helper/utils"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/node"
	"github.com/naoina/toml"

	bzzapi "github.com/5uwifi/canchain/basis/swarm/api"
)

const SWARM_VERSION = "0.3"

var (
	//flag definition for the dumpconfig command
	DumpConfigCommand = cli.Command{
		Action:      utils.MigrateFlags(dumpConfig),
		Name:        "dumpconfig",
		Usage:       "Show configuration values",
		ArgsUsage:   "",
		Flags:       app.Flags,
		Category:    "MISCELLANEOUS COMMANDS",
		Description: `The dumpconfig command shows configuration values.`,
	}

	//flag definition for the config file command
	SwarmTomlConfigPathFlag = cli.StringFlag{
		Name:  "config",
		Usage: "TOML configuration file",
	}
)

const (
	SWARM_ENV_CHEQUEBOOK_ADDR      = "SWARM_CHEQUEBOOK_ADDR"
	SWARM_ENV_ACCOUNT              = "SWARM_ACCOUNT"
	SWARM_ENV_LISTEN_ADDR          = "SWARM_LISTEN_ADDR"
	SWARM_ENV_PORT                 = "SWARM_PORT"
	SWARM_ENV_NETWORK_ID           = "SWARM_NETWORK_ID"
	SWARM_ENV_SWAP_ENABLE          = "SWARM_SWAP_ENABLE"
	SWARM_ENV_SWAP_API             = "SWARM_SWAP_API"
	SWARM_ENV_SYNC_DISABLE         = "SWARM_SYNC_DISABLE"
	SWARM_ENV_SYNC_UPDATE_DELAY    = "SWARM_ENV_SYNC_UPDATE_DELAY"
	SWARM_ENV_DELIVERY_SKIP_CHECK  = "SWARM_DELIVERY_SKIP_CHECK"
	SWARM_ENV_ENS_API              = "SWARM_ENS_API"
	SWARM_ENV_ENS_ADDR             = "SWARM_ENS_ADDR"
	SWARM_ENV_CORS                 = "SWARM_CORS"
	SWARM_ENV_BOOTNODES            = "SWARM_BOOTNODES"
	SWARM_ENV_PSS_ENABLE           = "SWARM_PSS_ENABLE"
	SWARM_ENV_STORE_PATH           = "SWARM_STORE_PATH"
	SWARM_ENV_STORE_CAPACITY       = "SWARM_STORE_CAPACITY"
	SWARM_ENV_STORE_CACHE_CAPACITY = "SWARM_STORE_CACHE_CAPACITY"
	GETH_ENV_DATADIR               = "GETH_DATADIR"
)

var tomlSettings = toml.Config{
	NormFieldName: func(rt reflect.Type, key string) string {
		return key
	},
	FieldToKey: func(rt reflect.Type, field string) string {
		return field
	},
	MissingField: func(rt reflect.Type, field string) error {
		link := ""
		if unicode.IsUpper(rune(rt.Name()[0])) && rt.PkgPath() != "main" {
			link = fmt.Sprintf(", check github.com/5uwifi/canchain/basis/swarm/api/config.go for available fields")
		}
		return fmt.Errorf("field '%s' is not defined in %s%s", field, rt.String(), link)
	},
}

func buildConfig(ctx *cli.Context) (config *bzzapi.Config, err error) {
	//start by creating a default config
	config = bzzapi.NewConfig()
	//first load settings from config file (if provided)
	config, err = configFileOverride(config, ctx)
	if err != nil {
		return nil, err
	}
	//override settings provided by environment variables
	config = envVarsOverride(config)
	//override settings provided by command line
	config = cmdLineOverride(config, ctx)
	//validate configuration parameters
	err = validateConfig(config)

	return
}

func initSwarmNode(config *bzzapi.Config, stack *node.Node, ctx *cli.Context) {
	//at this point, all vars should be set in the Config
	//get the account for the provided swarm account
	prvkey := getAccount(config.BzzAccount, ctx, stack)
	//set the resolved config path (geth --datadir)
	config.Path = stack.InstanceDir()
	//finally, initialize the configuration
	config.Init(prvkey)
	//configuration phase completed here
	log4j.Debug("Starting Swarm with the following parameters:")
	//after having created the config, print it to screen
	log4j.Debug(printConfig(config))
}

func configFileOverride(config *bzzapi.Config, ctx *cli.Context) (*bzzapi.Config, error) {
	var err error

	//only do something if the -config flag has been set
	if ctx.GlobalIsSet(SwarmTomlConfigPathFlag.Name) {
		var filepath string
		if filepath = ctx.GlobalString(SwarmTomlConfigPathFlag.Name); filepath == "" {
			utils.Fatalf("Config file flag provided with invalid file path")
		}
		f, err := os.Open(filepath)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		//decode the TOML file into a Config struct
		//note that we are decoding into the existing defaultConfig;
		//if an entry is not present in the file, the default entry is kept
		err = tomlSettings.NewDecoder(f).Decode(&config)
		// Add file name to errors that have a line number.
		if _, ok := err.(*toml.LineError); ok {
			err = errors.New(filepath + ", " + err.Error())
		}
	}
	return config, err
}

func cmdLineOverride(currentConfig *bzzapi.Config, ctx *cli.Context) *bzzapi.Config {

	if keyid := ctx.GlobalString(SwarmAccountFlag.Name); keyid != "" {
		currentConfig.BzzAccount = keyid
	}

	if chbookaddr := ctx.GlobalString(ChequebookAddrFlag.Name); chbookaddr != "" {
		currentConfig.Contract = common.HexToAddress(chbookaddr)
	}

	if networkid := ctx.GlobalString(SwarmNetworkIdFlag.Name); networkid != "" {
		if id, _ := strconv.Atoi(networkid); id != 0 {
			currentConfig.NetworkID = uint64(id)
		}
	}

	if ctx.GlobalIsSet(utils.DataDirFlag.Name) {
		if datadir := ctx.GlobalString(utils.DataDirFlag.Name); datadir != "" {
			currentConfig.Path = datadir
		}
	}

	bzzport := ctx.GlobalString(SwarmPortFlag.Name)
	if len(bzzport) > 0 {
		currentConfig.Port = bzzport
	}

	if bzzaddr := ctx.GlobalString(SwarmListenAddrFlag.Name); bzzaddr != "" {
		currentConfig.ListenAddr = bzzaddr
	}

	if ctx.GlobalIsSet(SwarmSwapEnabledFlag.Name) {
		currentConfig.SwapEnabled = true
	}

	if ctx.GlobalIsSet(SwarmSyncDisabledFlag.Name) {
		currentConfig.SyncEnabled = false
	}

	if d := ctx.GlobalDuration(SwarmSyncUpdateDelay.Name); d > 0 {
		currentConfig.SyncUpdateDelay = d
	}

	if ctx.GlobalIsSet(SwarmDeliverySkipCheckFlag.Name) {
		currentConfig.DeliverySkipCheck = true
	}

	currentConfig.SwapAPI = ctx.GlobalString(SwarmSwapAPIFlag.Name)
	if currentConfig.SwapEnabled && currentConfig.SwapAPI == "" {
		utils.Fatalf(SWARM_ERR_SWAP_SET_NO_API)
	}

	if ctx.GlobalIsSet(EnsAPIFlag.Name) {
		ensAPIs := ctx.GlobalStringSlice(EnsAPIFlag.Name)
		// preserve backward compatibility to disable ENS with --ens-api=""
		if len(ensAPIs) == 1 && ensAPIs[0] == "" {
			ensAPIs = nil
		}
		currentConfig.EnsAPIs = ensAPIs
	}

	if cors := ctx.GlobalString(CorsStringFlag.Name); cors != "" {
		currentConfig.Cors = cors
	}

	if ctx.GlobalIsSet(utils.BootnodesFlag.Name) {
		currentConfig.BootNodes = ctx.GlobalString(utils.BootnodesFlag.Name)
	}

	if storePath := ctx.GlobalString(SwarmStorePath.Name); storePath != "" {
		currentConfig.LocalStoreParams.ChunkDbPath = storePath
	}

	if storeCapacity := ctx.GlobalUint64(SwarmStoreCapacity.Name); storeCapacity != 0 {
		currentConfig.LocalStoreParams.DbCapacity = storeCapacity
	}

	if storeCacheCapacity := ctx.GlobalUint(SwarmStoreCacheCapacity.Name); storeCacheCapacity != 0 {
		currentConfig.LocalStoreParams.CacheCapacity = storeCacheCapacity
	}

	return currentConfig

}

func envVarsOverride(currentConfig *bzzapi.Config) (config *bzzapi.Config) {

	if keyid := os.Getenv(SWARM_ENV_ACCOUNT); keyid != "" {
		currentConfig.BzzAccount = keyid
	}

	if chbookaddr := os.Getenv(SWARM_ENV_CHEQUEBOOK_ADDR); chbookaddr != "" {
		currentConfig.Contract = common.HexToAddress(chbookaddr)
	}

	if networkid := os.Getenv(SWARM_ENV_NETWORK_ID); networkid != "" {
		if id, _ := strconv.Atoi(networkid); id != 0 {
			currentConfig.NetworkID = uint64(id)
		}
	}

	if datadir := os.Getenv(GETH_ENV_DATADIR); datadir != "" {
		currentConfig.Path = datadir
	}

	bzzport := os.Getenv(SWARM_ENV_PORT)
	if len(bzzport) > 0 {
		currentConfig.Port = bzzport
	}

	if bzzaddr := os.Getenv(SWARM_ENV_LISTEN_ADDR); bzzaddr != "" {
		currentConfig.ListenAddr = bzzaddr
	}

	if swapenable := os.Getenv(SWARM_ENV_SWAP_ENABLE); swapenable != "" {
		if swap, err := strconv.ParseBool(swapenable); err != nil {
			currentConfig.SwapEnabled = swap
		}
	}

	if syncdisable := os.Getenv(SWARM_ENV_SYNC_DISABLE); syncdisable != "" {
		if sync, err := strconv.ParseBool(syncdisable); err != nil {
			currentConfig.SyncEnabled = !sync
		}
	}

	if v := os.Getenv(SWARM_ENV_DELIVERY_SKIP_CHECK); v != "" {
		if skipCheck, err := strconv.ParseBool(v); err != nil {
			currentConfig.DeliverySkipCheck = skipCheck
		}
	}

	if v := os.Getenv(SWARM_ENV_SYNC_UPDATE_DELAY); v != "" {
		if d, err := time.ParseDuration(v); err != nil {
			currentConfig.SyncUpdateDelay = d
		}
	}

	if swapapi := os.Getenv(SWARM_ENV_SWAP_API); swapapi != "" {
		currentConfig.SwapAPI = swapapi
	}

	if currentConfig.SwapEnabled && currentConfig.SwapAPI == "" {
		utils.Fatalf(SWARM_ERR_SWAP_SET_NO_API)
	}

	if ensapi := os.Getenv(SWARM_ENV_ENS_API); ensapi != "" {
		currentConfig.EnsAPIs = strings.Split(ensapi, ",")
	}

	if ensaddr := os.Getenv(SWARM_ENV_ENS_ADDR); ensaddr != "" {
		currentConfig.EnsRoot = common.HexToAddress(ensaddr)
	}

	if cors := os.Getenv(SWARM_ENV_CORS); cors != "" {
		currentConfig.Cors = cors
	}

	if bootnodes := os.Getenv(SWARM_ENV_BOOTNODES); bootnodes != "" {
		currentConfig.BootNodes = bootnodes
	}

	return currentConfig
}

func dumpConfig(ctx *cli.Context) error {
	cfg, err := buildConfig(ctx)
	if err != nil {
		utils.Fatalf(fmt.Sprintf("Uh oh - dumpconfig triggered an error %v", err))
	}
	comment := ""
	out, err := tomlSettings.Marshal(&cfg)
	if err != nil {
		return err
	}
	io.WriteString(os.Stdout, comment)
	os.Stdout.Write(out)
	return nil
}

func validateConfig(cfg *bzzapi.Config) (err error) {
	for _, ensAPI := range cfg.EnsAPIs {
		if ensAPI != "" {
			if err := validateEnsAPIs(ensAPI); err != nil {
				return fmt.Errorf("invalid format [tld:][contract-addr@]url for ENS API endpoint configuration %q: %v", ensAPI, err)
			}
		}
	}
	return nil
}

func validateEnsAPIs(s string) (err error) {
	// missing contract address
	if strings.HasPrefix(s, "@") {
		return errors.New("missing contract address")
	}
	// missing url
	if strings.HasSuffix(s, "@") {
		return errors.New("missing url")
	}
	// missing tld
	if strings.HasPrefix(s, ":") {
		return errors.New("missing tld")
	}
	// missing url
	if strings.HasSuffix(s, ":") {
		return errors.New("missing url")
	}
	return nil
}

func printConfig(config *bzzapi.Config) string {
	out, err := tomlSettings.Marshal(&config)
	if err != nil {
		return fmt.Sprintf("Something is not right with the configuration: %v", err)
	}
	return string(out)
}
