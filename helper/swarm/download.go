package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/5uwifi/canchain/helper/utils"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/swarm/api"
	swarm "github.com/5uwifi/canchain/basis/swarm/api/client"
	"gopkg.in/urfave/cli.v1"
)

func download(ctx *cli.Context) {
	log4j.Debug("downloading content using swarm down")
	args := ctx.Args()
	dest := "."

	switch len(args) {
	case 0:
		utils.Fatalf("Usage: swarm down [options] <bzz locator> [<destination path>]")
	case 1:
		log4j.Trace(fmt.Sprintf("swarm down: no destination path - assuming working dir"))
	default:
		log4j.Trace(fmt.Sprintf("destination path arg: %s", args[1]))
		if absDest, err := filepath.Abs(args[1]); err == nil {
			dest = absDest
		} else {
			utils.Fatalf("could not get download path: %v", err)
		}
	}

	var (
		bzzapi      = strings.TrimRight(ctx.GlobalString(SwarmApiFlag.Name), "/")
		isRecursive = ctx.Bool(SwarmRecursiveFlag.Name)
		client      = swarm.NewClient(bzzapi)
	)

	if fi, err := os.Stat(dest); err == nil {
		if isRecursive && !fi.Mode().IsDir() {
			utils.Fatalf("destination path is not a directory!")
		}
	} else {
		if !os.IsNotExist(err) {
			utils.Fatalf("could not stat path: %v", err)
		}
	}

	uri, err := api.Parse(args[0])
	if err != nil {
		utils.Fatalf("could not parse uri argument: %v", err)
	}

	// assume behaviour according to --recursive switch
	if isRecursive {
		if err := client.DownloadDirectory(uri.Addr, uri.Path, dest); err != nil {
			utils.Fatalf("encoutered an error while downloading directory: %v", err)
		}
	} else {
		// we are downloading a file
		log4j.Debug(fmt.Sprintf("downloading file/path from a manifest. hash: %s, path:%s", uri.Addr, uri.Path))

		err := client.DownloadFile(uri.Addr, uri.Path, dest)
		if err != nil {
			utils.Fatalf("could not download %s from given address: %s. error: %v", uri.Path, uri.Addr, err)
		}
	}
}
