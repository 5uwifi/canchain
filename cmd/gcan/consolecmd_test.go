package main

import (
	"crypto/rand"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/5uwifi/canchain/params"
)

const (
	ipcAPIs  = "admin:1.0 can:1.0 clique:1.0 debug:1.0 miner:1.0 net:1.0 personal:1.0 rpc:1.0 shh:1.0 txpool:1.0 web3:1.0"
	httpAPIs = "can:1.0 net:1.0 rpc:1.0 web3:1.0"
)

func TestConsoleWelcome(t *testing.T) {
	coinbase := "0x8605cdbbdb6d264aa742e77020dcbc58fcdce182"

	gcan := runGcan(t,
		"--port", "0", "--maxpeers", "0", "--nodiscover", "--nat", "none",
		"--canerbase", coinbase, "--shh",
		"console")

	gcan.SetTemplateFunc("goos", func() string { return runtime.GOOS })
	gcan.SetTemplateFunc("goarch", func() string { return runtime.GOARCH })
	gcan.SetTemplateFunc("gover", runtime.Version)
	gcan.SetTemplateFunc("gcanver", func() string { return params.VersionWithMeta })
	gcan.SetTemplateFunc("niltime", func() string { return time.Unix(1535009276, 0).Format(time.RFC1123) })
	gcan.SetTemplateFunc("apis", func() string { return ipcAPIs })

	gcan.Expect(`
Welcome to the Gcan JavaScript console!

instance: Gcan/v{{gcanver}}/{{goos}}-{{goarch}}/{{gover}}
coinbase: {{.Canerbase}}
at block: 0 ({{niltime}})
 datadir: {{.Datadir}}
 modules: {{apis}}

> {{.InputLine "exit"}}
`)
	gcan.ExpectExit()
}

func TestIPCAttachWelcome(t *testing.T) {
	coinbase := "0x8605cdbbdb6d264aa742e77020dcbc58fcdce182"
	var ipc string
	if runtime.GOOS == "windows" {
		ipc = `\\.\pipe\gcan` + strconv.Itoa(trulyRandInt(100000, 999999))
	} else {
		ws := tmpdir(t)
		defer os.RemoveAll(ws)
		ipc = filepath.Join(ws, "gcan.ipc")
	}
	gcan := runGcan(t,
		"--port", "0", "--maxpeers", "0", "--nodiscover", "--nat", "none",
		"--canerbase", coinbase, "--shh", "--ipcpath", ipc)

	time.Sleep(2 * time.Second)
	testAttachWelcome(t, gcan, "ipc:"+ipc, ipcAPIs)

	gcan.Interrupt()
	gcan.ExpectExit()
}

func TestHTTPAttachWelcome(t *testing.T) {
	coinbase := "0x8605cdbbdb6d264aa742e77020dcbc58fcdce182"
	port := strconv.Itoa(trulyRandInt(1024, 65536))
	gcan := runGcan(t,
		"--port", "0", "--maxpeers", "0", "--nodiscover", "--nat", "none",
		"--canerbase", coinbase, "--rpc", "--rpcport", port)

	time.Sleep(2 * time.Second)
	testAttachWelcome(t, gcan, "http://localhost:"+port, httpAPIs)

	gcan.Interrupt()
	gcan.ExpectExit()
}

func TestWSAttachWelcome(t *testing.T) {
	coinbase := "0x8605cdbbdb6d264aa742e77020dcbc58fcdce182"
	port := strconv.Itoa(trulyRandInt(1024, 65536))

	gcan := runGcan(t,
		"--port", "0", "--maxpeers", "0", "--nodiscover", "--nat", "none",
		"--canerbase", coinbase, "--ws", "--wsport", port)

	time.Sleep(2 * time.Second)
	testAttachWelcome(t, gcan, "ws://localhost:"+port, httpAPIs)

	gcan.Interrupt()
	gcan.ExpectExit()
}

func testAttachWelcome(t *testing.T, gcan *testGcan, endpoint, apis string) {
	attach := runGcan(t, "attach", endpoint)
	defer attach.ExpectExit()
	attach.CloseStdin()

	attach.SetTemplateFunc("goos", func() string { return runtime.GOOS })
	attach.SetTemplateFunc("goarch", func() string { return runtime.GOARCH })
	attach.SetTemplateFunc("gover", runtime.Version)
	attach.SetTemplateFunc("gcanver", func() string { return params.VersionWithMeta })
	attach.SetTemplateFunc("canerbase", func() string { return gcan.Canerbase })
	attach.SetTemplateFunc("niltime", func() string { return time.Unix(1535009276, 0).Format(time.RFC1123) })
	attach.SetTemplateFunc("ipc", func() bool { return strings.HasPrefix(endpoint, "ipc") })
	attach.SetTemplateFunc("datadir", func() string { return gcan.Datadir })
	attach.SetTemplateFunc("apis", func() string { return apis })

	attach.Expect(`
Welcome to the Gcan JavaScript console!

instance: Gcan/v{{gcanver}}/{{goos}}-{{goarch}}/{{gover}}
coinbase: {{canerbase}}
at block: 0 ({{niltime}}){{if ipc}}
 datadir: {{datadir}}{{end}}
 modules: {{apis}}

> {{.InputLine "exit" }}
`)
	attach.ExpectExit()
}

func trulyRandInt(lo, hi int) int {
	num, _ := rand.Int(rand.Reader, big.NewInt(int64(hi-lo)))
	return int(num.Int64()) + lo
}
