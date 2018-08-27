package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/docker/docker/pkg/reexec"
	"github.com/5uwifi/canchain/privacy/cmdtest"
)

func tmpdir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "gcan-test")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type testGcan struct {
	*cmdtest.TestCmd

	Datadir   string
	Canerbase string
}

func init() {
	reexec.Register("gcan-test", func() {
		if err := app.Run(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	})
}

func TestMain(m *testing.M) {
	if reexec.Init() {
		return
	}
	os.Exit(m.Run())
}

func runGcan(t *testing.T, args ...string) *testGcan {
	tt := &testGcan{}
	tt.TestCmd = cmdtest.NewTestCmd(t, tt)
	for i, arg := range args {
		switch {
		case arg == "-datadir" || arg == "--datadir":
			if i < len(args)-1 {
				tt.Datadir = args[i+1]
			}
		case arg == "-canerbase" || arg == "--canerbase":
			if i < len(args)-1 {
				tt.Canerbase = args[i+1]
			}
		}
	}
	if tt.Datadir == "" {
		tt.Datadir = tmpdir(t)
		tt.Cleanup = func() { os.RemoveAll(tt.Datadir) }
		args = append([]string{"-datadir", tt.Datadir}, args...)
		defer func() {
			if t.Failed() {
				tt.Cleanup()
			}
		}()
	}

	tt.Run("gcan-test", args...)

	return tt
}
