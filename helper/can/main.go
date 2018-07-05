//
// (at your option) any later version.
//
//

package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	godebug "runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/gosigar"
	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/accounts/keystore"
	"github.com/5uwifi/canchain/helper/utils"
	"github.com/5uwifi/canchain/repl"
	"github.com/5uwifi/canchain/ledger"
	"github.com/5uwifi/canchain/canclient"
	"github.com/5uwifi/canchain/internal/debug"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/metrics"
	"github.com/5uwifi/canchain/node"
	"gopkg.in/urfave/cli.v1"
)

const (
	clientIdentifier = "can" // Client identifier to advertise over the network
)

var (
	// Git SHA1 commit hash of the release (set via linker flags)
	gitCommit = ""
	// The app that holds all commands and flags.
	app = utils.NewApp(gitCommit, "the CANChain command line interface")
	// flags that configure the node
	nodeFlags = []cli.Flag{
		utils.IdentityFlag,
		utils.UnlockedAccountFlag,
		utils.PasswordFileFlag,
		utils.BootnodesFlag,
		utils.BootnodesV4Flag,
		utils.BootnodesV5Flag,
		utils.DataDirFlag,
		utils.KeyStoreDirFlag,
		utils.NoUSBFlag,
		utils.CanhashCacheDirFlag,
		utils.CanhashCachesInMemoryFlag,
		utils.CanhashCachesOnDiskFlag,
		utils.CanhashDatasetDirFlag,
		utils.CanhashDatasetsInMemoryFlag,
		utils.CanhashDatasetsOnDiskFlag,
		utils.TxPoolNoLocalsFlag,
		utils.TxPoolJournalFlag,
		utils.TxPoolRejournalFlag,
		utils.TxPoolPriceLimitFlag,
		utils.TxPoolPriceBumpFlag,
		utils.TxPoolAccountSlotsFlag,
		utils.TxPoolGlobalSlotsFlag,
		utils.TxPoolAccountQueueFlag,
		utils.TxPoolGlobalQueueFlag,
		utils.TxPoolLifetimeFlag,
		utils.SyncModeFlag,
		utils.GCModeFlag,
		utils.LightKDFFlag,
		utils.CacheFlag,
		utils.CacheDatabaseFlag,
		utils.CacheGCFlag,
		utils.TrieCacheGenFlag,
		utils.ListenPortFlag,
		utils.MaxPeersFlag,
		utils.MaxPendingPeersFlag,
		utils.CanerbaseFlag,
		utils.FeePriceFlag,
		utils.MinerThreadsFlag,
		utils.MiningEnabledFlag,
		utils.TargetGasLimitFlag,
		utils.NATFlag,
		utils.NoDiscoverFlag,
		utils.DiscoveryV5Flag,
		utils.NetrestrictFlag,
		utils.NodeKeyFileFlag,
		utils.NodeKeyHexFlag,
		utils.DeveloperFlag,
		utils.DeveloperPeriodFlag,
		utils.TestnetFlag,
		utils.VMEnableDebugFlag,
		utils.NetworkIdFlag,
		utils.RPCCORSDomainFlag,
		utils.RPCVirtualHostsFlag,
		utils.CANStatsURLFlag,
		utils.MetricsEnabledFlag,
		utils.FakePoWFlag,
		utils.NoCompactionFlag,
		utils.GpoBlocksFlag,
		utils.GpoPercentileFlag,
		utils.ExtraDataFlag,
		configFileFlag,
	}

	rpcFlags = []cli.Flag{
		utils.RPCEnabledFlag,
		utils.RPCListenAddrFlag,
		utils.RPCPortFlag,
		utils.RPCApiFlag,
		utils.WSEnabledFlag,
		utils.WSListenAddrFlag,
		utils.WSPortFlag,
		utils.WSApiFlag,
		utils.WSAllowedOriginsFlag,
		utils.IPCDisabledFlag,
		utils.IPCPathFlag,
	}

	whisperFlags = []cli.Flag{
		utils.WhisperEnabledFlag,
		utils.WhisperMaxMessageSizeFlag,
		utils.WhisperMinPOWFlag,
	}
)

func init() {
	// Initialize the CLI app and start Geth
	app.Action = can
	app.HideVersion = true // we have a command to print the version
	app.Copyright = "Copyright 2018 The CANChain Authors"
	app.Commands = []cli.Command{
		// See chaincmd.go:
		initCommand,
		importCommand,
		exportCommand,
		importPreimagesCommand,
		exportPreimagesCommand,
		copydbCommand,
		removedbCommand,
		dumpCommand,
		// See monitorcmd.go:
		monitorCommand,
		// See accountcmd.go:
		accountCommand,
		walletCommand,
		// See consolecmd.go:
		consoleCommand,
		attachCommand,
		javascriptCommand,
		// See misccmd.go:
		makecacheCommand,
		makedagCommand,
		versionCommand,
		bugCommand,
		licenseCommand,
		// See config.go
		dumpConfigCommand,
	}
	sort.Sort(cli.CommandsByName(app.Commands))

	app.Flags = append(app.Flags, nodeFlags...)
	app.Flags = append(app.Flags, rpcFlags...)
	app.Flags = append(app.Flags, consoleFlags...)
	app.Flags = append(app.Flags, debug.Flags...)
	app.Flags = append(app.Flags, whisperFlags...)

	app.Before = func(ctx *cli.Context) error {
		runtime.GOMAXPROCS(runtime.NumCPU())
		if err := debug.Setup(ctx); err != nil {
			return err
		}
		// Cap the cache allowance and tune the garbage colelctor
		var mem gosigar.Mem
		if err := mem.Get(); err == nil {
			allowance := int(mem.Total / 1024 / 1024 / 3)
			if cache := ctx.GlobalInt(utils.CacheFlag.Name); cache > allowance {
				log4j.Warn("Sanitizing cache to Go's GC limits", "provided", cache, "updated", allowance)
				ctx.GlobalSet(utils.CacheFlag.Name, strconv.Itoa(allowance))
			}
		}
		// Ensure Go's GC ignores the database cache for trigger percentage
		cache := ctx.GlobalInt(utils.CacheFlag.Name)
		gogc := math.Max(20, math.Min(100, 100/(float64(cache)/1024)))

		log4j.Debug("Sanitizing Go's GC trigger", "percent", int(gogc))
		godebug.SetGCPercent(int(gogc))

		// Start system runtime metrics collection
		go metrics.CollectProcessMetrics(3 * time.Second)

		utils.SetupNetwork(ctx)
		return nil
	}

	app.After = func(ctx *cli.Context) error {
		debug.Exit()
		repl.Stdin.Close() // Resets terminal mode.
		return nil
	}
}

func main() {
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func can(ctx *cli.Context) error {
	node := makeFullNode(ctx)
	startNode(ctx, node)
	node.Wait()
	return nil
}

func startNode(ctx *cli.Context, stack *node.Node) {
	debug.Memsize.Add("node", stack)

	// Start up the node itself
	utils.StartNode(stack)

	// Unlock any account specifically requested
	ks := stack.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)

	passwords := utils.MakePasswordList(ctx)
	unlocks := strings.Split(ctx.GlobalString(utils.UnlockedAccountFlag.Name), ",")
	for i, account := range unlocks {
		if trimmed := strings.TrimSpace(account); trimmed != "" {
			unlockAccount(ctx, ks, trimmed, i, passwords)
		}
	}
	// Register wallet event handlers to open and auto-derive wallets
	events := make(chan accounts.WalletEvent, 16)
	stack.AccountManager().Subscribe(events)

	go func() {
		// Create a chain state reader for self-derivation
		rpcClient, err := stack.Attach()
		if err != nil {
			utils.Fatalf("Failed to attach to self: %v", err)
		}
		stateReader := canclient.NewClient(rpcClient)

		// Open any wallets already attached
		for _, wallet := range stack.AccountManager().Wallets() {
			if err := wallet.Open(""); err != nil {
				log4j.Warn("Failed to open wallet", "url", wallet.URL(), "err", err)
			}
		}
		// Listen for wallet event till termination
		for event := range events {
			switch event.Kind {
			case accounts.WalletArrived:
				if err := event.Wallet.Open(""); err != nil {
					log4j.Warn("New wallet appeared, failed to open", "url", event.Wallet.URL(), "err", err)
				}
			case accounts.WalletOpened:
				status, _ := event.Wallet.Status()
				log4j.Info("New wallet appeared", "url", event.Wallet.URL(), "status", status)

				if event.Wallet.URL().Scheme == "ledger" {
					event.Wallet.SelfDerive(accounts.DefaultLedgerBaseDerivationPath, stateReader)
				} else {
					event.Wallet.SelfDerive(accounts.DefaultBaseDerivationPath, stateReader)
				}

			case accounts.WalletDropped:
				log4j.Info("Old wallet dropped", "url", event.Wallet.URL())
				event.Wallet.Close()
			}
		}
	}()
	// Start auxiliary services if enabled
	if ctx.GlobalBool(utils.MiningEnabledFlag.Name) || ctx.GlobalBool(utils.DeveloperFlag.Name) {
		var canchain *ledger.Ledger
		if err := stack.Service(&canchain); err != nil {
			utils.Fatalf("CANChain service not running: %v", err)
		}
		// Use a reduced number of threads if requested
		if threads := ctx.GlobalInt(utils.MinerThreadsFlag.Name); threads > 0 {
			type threaded interface {
				SetThreads(threads int)
			}
			if th, ok := canchain.Engine().(threaded); ok {
				th.SetThreads(threads)
			}
		}
		// Set the gas price to the limits from the CLI and start mining
		canchain.TxPool().SetGasPrice(utils.GlobalBig(ctx, utils.FeePriceFlag.Name))
		if err := canchain.StartMining(true); err != nil {
			utils.Fatalf("Failed to start mining: %v", err)
		}
	}
}
