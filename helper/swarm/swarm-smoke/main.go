
package main

import (
	"os"
	"sort"

	"github.com/5uwifi/canchain/basis/log4j"
	colorable "github.com/mattn/go-colorable"

	cli "gopkg.in/urfave/cli.v1"
)

var (
	endpoints        []string
	includeLocalhost bool
	cluster          string
	scheme           string
	filesize         int
	from             int
	to               int
)

func main() {
	log4j.PrintOrigins(true)
	log4j.Root().SetHandler(log4j.LvlFilterHandler(log4j.LvlTrace, log4j.StreamHandler(colorable.NewColorableStderr(), log4j.TerminalFormat(true))))

	app := cli.NewApp()
	app.Name = "smoke-test"
	app.Usage = ""

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:        "cluster-endpoint",
			Value:       "testing",
			Usage:       "cluster to point to (open, or testing)",
			Destination: &cluster,
		},
		cli.IntFlag{
			Name:        "cluster-from",
			Value:       8501,
			Usage:       "swarm node (from)",
			Destination: &from,
		},
		cli.IntFlag{
			Name:        "cluster-to",
			Value:       8512,
			Usage:       "swarm node (to)",
			Destination: &to,
		},
		cli.StringFlag{
			Name:        "cluster-scheme",
			Value:       "http",
			Usage:       "http or https",
			Destination: &scheme,
		},
		cli.BoolFlag{
			Name:        "include-localhost",
			Usage:       "whether to include localhost:8500 as an endpoint",
			Destination: &includeLocalhost,
		},
		cli.IntFlag{
			Name:        "filesize",
			Value:       1,
			Usage:       "file size for generated random file in MB",
			Destination: &filesize,
		},
	}

	app.Commands = []cli.Command{
		{
			Name:    "upload_and_sync",
			Aliases: []string{"c"},
			Usage:   "upload and sync",
			Action:  cliUploadAndSync,
		},
	}

	sort.Sort(cli.FlagsByName(app.Flags))
	sort.Sort(cli.CommandsByName(app.Commands))

	err := app.Run(os.Args)
	if err != nil {
		log4j.Error(err.Error())
	}
}
