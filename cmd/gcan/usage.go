package main

import (
	"io"
	"sort"

	"strings"

	"github.com/5uwifi/canchain/cmd/utils"
	"github.com/5uwifi/canchain/privacy/debug"
	"gopkg.in/urfave/cli.v1"
)

var AppHelpTemplate = `NAME:
   {{.App.Name}} - {{.App.Usage}}

   Copyright 2018 The CANChain Authors

USAGE:
   {{.App.HelpName}} [options]{{if .App.Commands}} command [command options]{{end}} {{if .App.ArgsUsage}}{{.App.ArgsUsage}}{{else}}[arguments...]{{end}}
   {{if .App.Version}}
VERSION:
   {{.App.Version}}
   {{end}}{{if len .App.Authors}}
AUTHOR(S):
   {{range .App.Authors}}{{ . }}{{end}}
   {{end}}{{if .App.Commands}}
COMMANDS:
   {{range .App.Commands}}{{join .Names ", "}}{{ "\t" }}{{.Usage}}
   {{end}}{{end}}{{if .FlagGroups}}
{{range .FlagGroups}}{{.Name}} OPTIONS:
  {{range .Flags}}{{.}}
  {{end}}
{{end}}{{end}}{{if .App.Copyright }}
COPYRIGHT:
   {{.App.Copyright}}
   {{end}}
`

type flagGroup struct {
	Name  string
	Flags []cli.Flag
}

var AppHelpFlagGroups = []flagGroup{
	{
		Name: "CANCHAIN",
		Flags: []cli.Flag{
			configFileFlag,
			utils.DataDirFlag,
			utils.KeyStoreDirFlag,
			utils.NoUSBFlag,
			utils.NetworkIdFlag,
			utils.TestnetFlag,
			utils.IronmanFlag,
			utils.SyncModeFlag,
			utils.GCModeFlag,
			utils.EthStatsURLFlag,
			utils.IdentityFlag,
			utils.LightServFlag,
			utils.LightPeersFlag,
			utils.LightKDFFlag,
		},
	},
	{
		Name: "DEVELOPER CHAIN",
		Flags: []cli.Flag{
			utils.DeveloperFlag,
			utils.DeveloperPeriodFlag,
		},
	},
	{
		Name: "ETHASH",
		Flags: []cli.Flag{
			utils.EthashCacheDirFlag,
			utils.EthashCachesInMemoryFlag,
			utils.EthashCachesOnDiskFlag,
			utils.EthashDatasetDirFlag,
			utils.EthashDatasetsInMemoryFlag,
			utils.EthashDatasetsOnDiskFlag,
		},
	},
	{
		Name: "TRANSACTION POOL",
		Flags: []cli.Flag{
			utils.TxPoolLocalsFlag,
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
		},
	},
	{
		Name: "PERFORMANCE TUNING",
		Flags: []cli.Flag{
			utils.CacheFlag,
			utils.CacheDatabaseFlag,
			utils.CacheTrieFlag,
			utils.CacheGCFlag,
			utils.TrieCacheGenFlag,
		},
	},
	{
		Name: "ACCOUNT",
		Flags: []cli.Flag{
			utils.UnlockedAccountFlag,
			utils.PasswordFileFlag,
		},
	},
	{
		Name: "API AND CONSOLE",
		Flags: []cli.Flag{
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
			utils.RPCCORSDomainFlag,
			utils.RPCVirtualHostsFlag,
			utils.JSpathFlag,
			utils.ExecFlag,
			utils.PreloadJSFlag,
		},
	},
	{
		Name: "NETWORKING",
		Flags: []cli.Flag{
			utils.BootnodesFlag,
			utils.BootnodesV4Flag,
			utils.BootnodesV5Flag,
			utils.ListenPortFlag,
			utils.MaxPeersFlag,
			utils.MaxPendingPeersFlag,
			utils.NATFlag,
			utils.NoDiscoverFlag,
			utils.DiscoveryV5Flag,
			utils.NetrestrictFlag,
			utils.NodeKeyFileFlag,
			utils.NodeKeyHexFlag,
		},
	},
	{
		Name: "MINER",
		Flags: []cli.Flag{
			utils.MiningEnabledFlag,
			utils.MinerThreadsFlag,
			utils.MinerNotifyFlag,
			utils.MinerGasPriceFlag,
			utils.MinerGasTargetFlag,
			utils.MinerGasLimitFlag,
			utils.MinerCanerbaseFlag,
			utils.MinerExtraDataFlag,
			utils.MinerRecommitIntervalFlag,
			utils.MinerNoVerfiyFlag,
		},
	},
	{
		Name: "GAS PRICE ORACLE",
		Flags: []cli.Flag{
			utils.GpoBlocksFlag,
			utils.GpoPercentileFlag,
		},
	},
	{
		Name: "VIRTUAL MACHINE",
		Flags: []cli.Flag{
			utils.VMEnableDebugFlag,
			utils.EVMInterpreterFlag,
			utils.EWASMInterpreterFlag,
		},
	},
	{
		Name: "LOGGING AND DEBUGGING",
		Flags: append([]cli.Flag{
			utils.FakePoWFlag,
			utils.NoCompactionFlag,
		}, debug.Flags...),
	},
	{
		Name: "METRICS AND STATS",
		Flags: []cli.Flag{
			utils.MetricsEnabledFlag,
			utils.MetricsEnableInfluxDBFlag,
			utils.MetricsInfluxDBEndpointFlag,
			utils.MetricsInfluxDBDatabaseFlag,
			utils.MetricsInfluxDBUsernameFlag,
			utils.MetricsInfluxDBPasswordFlag,
			utils.MetricsInfluxDBHostTagFlag,
		},
	},
	{
		Name:  "WHISPER (EXPERIMENTAL)",
		Flags: whisperFlags,
	},
	{
		Name: "DEPRECATED",
		Flags: []cli.Flag{
			utils.MinerLegacyThreadsFlag,
			utils.MinerLegacyGasTargetFlag,
			utils.MinerLegacyGasPriceFlag,
			utils.MinerLegacyCanerbaseFlag,
			utils.MinerLegacyExtraDataFlag,
		},
	},
	{
		Name: "MISC",
	},
}

type byCategory []flagGroup

func (a byCategory) Len() int      { return len(a) }
func (a byCategory) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byCategory) Less(i, j int) bool {
	iCat, jCat := a[i].Name, a[j].Name
	iIdx, jIdx := len(AppHelpFlagGroups), len(AppHelpFlagGroups)

	for i, group := range AppHelpFlagGroups {
		if iCat == group.Name {
			iIdx = i
		}
		if jCat == group.Name {
			jIdx = i
		}
	}

	return iIdx < jIdx
}

func flagCategory(flag cli.Flag) string {
	for _, category := range AppHelpFlagGroups {
		for _, flg := range category.Flags {
			if flg.GetName() == flag.GetName() {
				return category.Name
			}
		}
	}
	return "MISC"
}

func init() {
	cli.AppHelpTemplate = AppHelpTemplate

	type helpData struct {
		App        interface{}
		FlagGroups []flagGroup
	}

	originalHelpPrinter := cli.HelpPrinter
	cli.HelpPrinter = func(w io.Writer, tmpl string, data interface{}) {
		if tmpl == AppHelpTemplate {
			categorized := make(map[string]struct{})
			for _, group := range AppHelpFlagGroups {
				for _, flag := range group.Flags {
					categorized[flag.String()] = struct{}{}
				}
			}
			uncategorized := []cli.Flag{}
			for _, flag := range data.(*cli.App).Flags {
				if _, ok := categorized[flag.String()]; !ok {
					if strings.HasPrefix(flag.GetName(), "dashboard") {
						continue
					}
					uncategorized = append(uncategorized, flag)
				}
			}
			if len(uncategorized) > 0 {
				miscs := len(AppHelpFlagGroups[len(AppHelpFlagGroups)-1].Flags)
				AppHelpFlagGroups[len(AppHelpFlagGroups)-1].Flags = append(AppHelpFlagGroups[len(AppHelpFlagGroups)-1].Flags, uncategorized...)

				defer func() {
					AppHelpFlagGroups[len(AppHelpFlagGroups)-1].Flags = AppHelpFlagGroups[len(AppHelpFlagGroups)-1].Flags[:miscs]
				}()
			}
			originalHelpPrinter(w, tmpl, helpData{data, AppHelpFlagGroups})
		} else if tmpl == utils.CommandHelpTemplate {
			categorized := make(map[string][]cli.Flag)
			for _, flag := range data.(cli.Command).Flags {
				if _, ok := categorized[flag.String()]; !ok {
					categorized[flagCategory(flag)] = append(categorized[flagCategory(flag)], flag)
				}
			}

			sorted := make([]flagGroup, 0, len(categorized))
			for cat, flgs := range categorized {
				sorted = append(sorted, flagGroup{cat, flgs})
			}
			sort.Sort(byCategory(sorted))

			originalHelpPrinter(w, tmpl, map[string]interface{}{
				"cmd":              data,
				"categorizedFlags": sorted,
			})
		} else {
			originalHelpPrinter(w, tmpl, data)
		}
	}
}
