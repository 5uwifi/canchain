package node

import (
	"crypto/ecdsa"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/accounts/keystore"
	"github.com/5uwifi/canchain/accounts/usbwallet"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/rpc"
)

const (
	datadirPrivateKey      = "nodekey"
	datadirDefaultKeyStore = "keystore"
	datadirStaticNodes     = "static-nodes.json"
	datadirTrustedNodes    = "trusted-nodes.json"
	datadirNodeDatabase    = "nodes"
)

type Config struct {
	Name string `toml:"-"`

	UserIdent string `toml:",omitempty"`

	Version string `toml:"-"`

	DataDir string

	P2P p2p.Config

	KeyStoreDir string `toml:",omitempty"`

	UseLightweightKDF bool `toml:",omitempty"`

	NoUSB bool `toml:",omitempty"`

	IPCPath string `toml:",omitempty"`

	HTTPHost string `toml:",omitempty"`

	HTTPPort int `toml:",omitempty"`

	HTTPCors []string `toml:",omitempty"`

	HTTPVirtualHosts []string `toml:",omitempty"`

	HTTPModules []string `toml:",omitempty"`

	HTTPTimeouts rpc.HTTPTimeouts

	WSHost string `toml:",omitempty"`

	WSPort int `toml:",omitempty"`

	WSOrigins []string `toml:",omitempty"`

	WSModules []string `toml:",omitempty"`

	WSExposeAll bool `toml:",omitempty"`

	Logger log4j.Logger `toml:",omitempty"`
}

func (c *Config) IPCEndpoint() string {
	if c.IPCPath == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(c.IPCPath, `\\.\pipe\`) {
			return c.IPCPath
		}
		return `\\.\pipe\` + c.IPCPath
	}
	if filepath.Base(c.IPCPath) == c.IPCPath {
		if c.DataDir == "" {
			return filepath.Join(os.TempDir(), c.IPCPath)
		}
		return filepath.Join(c.DataDir, c.IPCPath)
	}
	return c.IPCPath
}

func (c *Config) NodeDB() string {
	if c.DataDir == "" {
		return ""
	}
	return c.ResolvePath(datadirNodeDatabase)
}

func DefaultIPCEndpoint(clientIdentifier string) string {
	if clientIdentifier == "" {
		clientIdentifier = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		if clientIdentifier == "" {
			panic("empty executable name")
		}
	}
	config := &Config{DataDir: DefaultDataDir(), IPCPath: clientIdentifier + ".ipc"}
	return config.IPCEndpoint()
}

func (c *Config) HTTPEndpoint() string {
	if c.HTTPHost == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", c.HTTPHost, c.HTTPPort)
}

func DefaultHTTPEndpoint() string {
	config := &Config{HTTPHost: DefaultHTTPHost, HTTPPort: DefaultHTTPPort}
	return config.HTTPEndpoint()
}

func (c *Config) WSEndpoint() string {
	if c.WSHost == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", c.WSHost, c.WSPort)
}

func DefaultWSEndpoint() string {
	config := &Config{WSHost: DefaultWSHost, WSPort: DefaultWSPort}
	return config.WSEndpoint()
}

func (c *Config) NodeName() string {
	name := c.name()
	if name == "gcan" || name == "gcan-testnet" {
		name = "Gcan"
	}
	if c.UserIdent != "" {
		name += "/" + c.UserIdent
	}
	if c.Version != "" {
		name += "/v" + c.Version
	}
	name += "/" + runtime.GOOS + "-" + runtime.GOARCH
	name += "/" + runtime.Version()
	return name
}

func (c *Config) name() string {
	if c.Name == "" {
		progname := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		if progname == "" {
			panic("empty executable name, set Config.Name")
		}
		return progname
	}
	return c.Name
}

var isOldGethResource = map[string]bool{
	"chaindata":          true,
	"nodes":              true,
	"nodekey":            true,
	"static-nodes.json":  true,
	"trusted-nodes.json": true,
}

func (c *Config) ResolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if c.DataDir == "" {
		return ""
	}
	if c.name() == "gcan" && isOldGethResource[path] {
		oldpath := ""
		if c.Name == "gcan" {
			oldpath = filepath.Join(c.DataDir, path)
		}
		if oldpath != "" && common.FileExist(oldpath) {
			return oldpath
		}
	}
	return filepath.Join(c.instanceDir(), path)
}

func (c *Config) instanceDir() string {
	if c.DataDir == "" {
		return ""
	}
	return filepath.Join(c.DataDir, c.name())
}

func (c *Config) NodeKey() *ecdsa.PrivateKey {
	if c.P2P.PrivateKey != nil {
		return c.P2P.PrivateKey
	}
	if c.DataDir == "" {
		key, err := crypto.GenerateKey()
		if err != nil {
			log4j.Crit(fmt.Sprintf("Failed to generate ephemeral node key: %v", err))
		}
		return key
	}

	keyfile := c.ResolvePath(datadirPrivateKey)
	if key, err := crypto.LoadECDSA(keyfile); err == nil {
		return key
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		log4j.Crit(fmt.Sprintf("Failed to generate node key: %v", err))
	}
	instanceDir := filepath.Join(c.DataDir, c.name())
	if err := os.MkdirAll(instanceDir, 0700); err != nil {
		log4j.Error(fmt.Sprintf("Failed to persist node key: %v", err))
		return key
	}
	keyfile = filepath.Join(instanceDir, datadirPrivateKey)
	if err := crypto.SaveECDSA(keyfile, key); err != nil {
		log4j.Error(fmt.Sprintf("Failed to persist node key: %v", err))
	}
	return key
}

func (c *Config) StaticNodes() []*discover.Node {
	return c.parsePersistentNodes(c.ResolvePath(datadirStaticNodes))
}

func (c *Config) TrustedNodes() []*discover.Node {
	return c.parsePersistentNodes(c.ResolvePath(datadirTrustedNodes))
}

func (c *Config) parsePersistentNodes(path string) []*discover.Node {
	if c.DataDir == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var nodelist []string
	if err := common.LoadJSON(path, &nodelist); err != nil {
		log4j.Error(fmt.Sprintf("Can't load node file %s: %v", path, err))
		return nil
	}
	var nodes []*discover.Node
	for _, url := range nodelist {
		if url == "" {
			continue
		}
		node, err := discover.ParseNode(url)
		if err != nil {
			log4j.Error(fmt.Sprintf("Node URL %s: %v\n", url, err))
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func (c *Config) AccountConfig() (int, int, string, error) {
	scryptN := keystore.StandardScryptN
	scryptP := keystore.StandardScryptP
	if c.UseLightweightKDF {
		scryptN = keystore.LightScryptN
		scryptP = keystore.LightScryptP
	}

	var (
		keydir string
		err    error
	)
	switch {
	case filepath.IsAbs(c.KeyStoreDir):
		keydir = c.KeyStoreDir
	case c.DataDir != "":
		if c.KeyStoreDir == "" {
			keydir = filepath.Join(c.DataDir, datadirDefaultKeyStore)
		} else {
			keydir, err = filepath.Abs(c.KeyStoreDir)
		}
	case c.KeyStoreDir != "":
		keydir, err = filepath.Abs(c.KeyStoreDir)
	}
	return scryptN, scryptP, keydir, err
}

func makeAccountManager(conf *Config) (*accounts.Manager, string, error) {
	scryptN, scryptP, keydir, err := conf.AccountConfig()
	var ephemeral string
	if keydir == "" {
		keydir, err = ioutil.TempDir("", "go-ethereum-keystore")
		ephemeral = keydir
	}

	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(keydir, 0700); err != nil {
		return nil, "", err
	}
	backends := []accounts.Backend{
		keystore.NewKeyStore(keydir, scryptN, scryptP),
	}
	if !conf.NoUSB {
		if ledgerhub, err := usbwallet.NewLedgerHub(); err != nil {
			log4j.Warn(fmt.Sprintf("Failed to start Ledger hub, disabling: %v", err))
		} else {
			backends = append(backends, ledgerhub)
		}
		if trezorhub, err := usbwallet.NewTrezorHub(); err != nil {
			log4j.Warn(fmt.Sprintf("Failed to start Trezor hub, disabling: %v", err))
		} else {
			backends = append(backends, trezorhub)
		}
	}
	return accounts.NewManager(backends...), ephemeral, nil
}
