package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/5uwifi/canchain/helper/utils"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/swarm/storage"
	"gopkg.in/urfave/cli.v1"
)

func dbExport(ctx *cli.Context) {
	args := ctx.Args()
	if len(args) != 3 {
		utils.Fatalf("invalid arguments, please specify both <chunkdb> (path to a local chunk database), <file> (path to write the tar archive to, - for stdout) and the base key")
	}

	store, err := openLDBStore(args[0], common.Hex2Bytes(args[2]))
	if err != nil {
		utils.Fatalf("error opening local chunk database: %s", err)
	}
	defer store.Close()

	var out io.Writer
	if args[1] == "-" {
		out = os.Stdout
	} else {
		f, err := os.Create(args[1])
		if err != nil {
			utils.Fatalf("error opening output file: %s", err)
		}
		defer f.Close()
		out = f
	}

	count, err := store.Export(out)
	if err != nil {
		utils.Fatalf("error exporting local chunk database: %s", err)
	}

	log4j.Info(fmt.Sprintf("successfully exported %d chunks", count))
}

func dbImport(ctx *cli.Context) {
	args := ctx.Args()
	if len(args) != 3 {
		utils.Fatalf("invalid arguments, please specify both <chunkdb> (path to a local chunk database), <file> (path to read the tar archive from, - for stdin) and the base key")
	}

	store, err := openLDBStore(args[0], common.Hex2Bytes(args[2]))
	if err != nil {
		utils.Fatalf("error opening local chunk database: %s", err)
	}
	defer store.Close()

	var in io.Reader
	if args[1] == "-" {
		in = os.Stdin
	} else {
		f, err := os.Open(args[1])
		if err != nil {
			utils.Fatalf("error opening input file: %s", err)
		}
		defer f.Close()
		in = f
	}

	count, err := store.Import(in)
	if err != nil {
		utils.Fatalf("error importing local chunk database: %s", err)
	}

	log4j.Info(fmt.Sprintf("successfully imported %d chunks", count))
}

func dbClean(ctx *cli.Context) {
	args := ctx.Args()
	if len(args) != 2 {
		utils.Fatalf("invalid arguments, please specify <chunkdb> (path to a local chunk database) and the base key")
	}

	store, err := openLDBStore(args[0], common.Hex2Bytes(args[1]))
	if err != nil {
		utils.Fatalf("error opening local chunk database: %s", err)
	}
	defer store.Close()

	store.Cleanup()
}

func openLDBStore(path string, basekey []byte) (*storage.LDBStore, error) {
	if _, err := os.Stat(filepath.Join(path, "CURRENT")); err != nil {
		return nil, fmt.Errorf("invalid chunkdb path: %s", err)
	}

	storeparams := storage.NewDefaultStoreParams()
	ldbparams := storage.NewLDBStoreParams(storeparams, path)
	ldbparams.BaseKey = basekey
	return storage.NewLDBStore(ldbparams)
}
