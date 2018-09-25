package adapters

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/docker/docker/pkg/reexec"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/rpc"
)

type Node interface {
	Addr() []byte

	Client() (*rpc.Client, error)

	ServeRPC(net.Conn) error

	Start(snapshots map[string][]byte) error

	Stop() error

	NodeInfo() *p2p.NodeInfo

	Snapshots() (map[string][]byte, error)
}

type NodeAdapter interface {
	Name() string

	NewNode(config *NodeConfig) (Node, error)
}

type NodeConfig struct {
	ID cnode.ID

	PrivateKey *ecdsa.PrivateKey

	EnableMsgEvents bool

	Name string

	Services []string

	Reachable func(id cnode.ID) bool

	Port uint16
}

type nodeConfigJSON struct {
	ID              string   `json:"id"`
	PrivateKey      string   `json:"private_key"`
	Name            string   `json:"name"`
	Services        []string `json:"services"`
	EnableMsgEvents bool     `json:"enable_msg_events"`
	Port            uint16   `json:"port"`
}

func (n *NodeConfig) MarshalJSON() ([]byte, error) {
	confJSON := nodeConfigJSON{
		ID:              n.ID.String(),
		Name:            n.Name,
		Services:        n.Services,
		Port:            n.Port,
		EnableMsgEvents: n.EnableMsgEvents,
	}
	if n.PrivateKey != nil {
		confJSON.PrivateKey = hex.EncodeToString(crypto.FromECDSA(n.PrivateKey))
	}
	return json.Marshal(confJSON)
}

func (n *NodeConfig) UnmarshalJSON(data []byte) error {
	var confJSON nodeConfigJSON
	if err := json.Unmarshal(data, &confJSON); err != nil {
		return err
	}

	if confJSON.ID != "" {
		if err := n.ID.UnmarshalText([]byte(confJSON.ID)); err != nil {
			return err
		}
	}

	if confJSON.PrivateKey != "" {
		key, err := hex.DecodeString(confJSON.PrivateKey)
		if err != nil {
			return err
		}
		privKey, err := crypto.ToECDSA(key)
		if err != nil {
			return err
		}
		n.PrivateKey = privKey
	}

	n.Name = confJSON.Name
	n.Services = confJSON.Services
	n.Port = confJSON.Port
	n.EnableMsgEvents = confJSON.EnableMsgEvents

	return nil
}

func (n *NodeConfig) Node() *cnode.Node {
	return cnode.NewV4(&n.PrivateKey.PublicKey, net.IP{127, 0, 0, 1}, int(n.Port), int(n.Port))
}

func RandomNodeConfig() *NodeConfig {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic("unable to generate key")
	}

	id := cnode.PubkeyToIDV4(&key.PublicKey)
	port, err := assignTCPPort()
	if err != nil {
		panic("unable to assign tcp port")
	}
	return &NodeConfig{
		ID:              id,
		Name:            fmt.Sprintf("node_%s", id.String()),
		PrivateKey:      key,
		Port:            port,
		EnableMsgEvents: true,
	}
}

func assignTCPPort() (uint16, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return 0, err
	}
	p, err := strconv.ParseInt(port, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint16(p), nil
}

type ServiceContext struct {
	RPCDialer

	NodeContext *node.ServiceContext
	Config      *NodeConfig
	Snapshot    []byte
}

type RPCDialer interface {
	DialRPC(id cnode.ID) (*rpc.Client, error)
}

type Services map[string]ServiceFunc

type ServiceFunc func(ctx *ServiceContext) (node.Service, error)

var serviceFuncs = make(Services)

func RegisterServices(services Services) {
	for name, f := range services {
		if _, exists := serviceFuncs[name]; exists {
			panic(fmt.Sprintf("node service already exists: %q", name))
		}
		serviceFuncs[name] = f
	}

	if reexec.Init() {
		os.Exit(0)
	}
}
