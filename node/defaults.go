package node

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/nat"
	"github.com/5uwifi/canchain/rpc"
)

const (
	DefaultHTTPHost = "localhost"
	DefaultHTTPPort = 8042
	DefaultWSHost   = "localhost"
	DefaultWSPort   = 8043
)

var DefaultConfig = Config{
	DataDir:          DefaultDataDir(),
	HTTPPort:         DefaultHTTPPort,
	HTTPModules:      []string{"net", "web3"},
	HTTPVirtualHosts: []string{"localhost"},
	HTTPTimeouts:     rpc.DefaultHTTPTimeouts,
	WSPort:           DefaultWSPort,
	WSModules:        []string{"net", "web3"},
	P2P: p2p.Config{
		ListenAddr: ":24242",
		MaxPeers:   25,
		NAT:        nat.Any(),
	},
}

func DefaultDataDir() string {
	home := homeDir()
	if home != "" {
		if runtime.GOOS == "darwin" {
			return filepath.Join(home, "Library", "CANChain")
		} else if runtime.GOOS == "windows" {
			return filepath.Join(home, "AppData", "Roaming", "CANChain")
		} else {
			return filepath.Join(home, ".canchain")
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
