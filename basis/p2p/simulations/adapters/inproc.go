//
// (at your option) any later version.
//
//

package adapters

import (
	"errors"
	"fmt"
	"math"
	"net"
	"sync"

	"github.com/5uwifi/canchain/basis/event"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/node"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/p2p/simulations/pipes"
	"github.com/5uwifi/canchain/rpc"
)

type SimAdapter struct {
	pipe     func() (net.Conn, net.Conn, error)
	mtx      sync.RWMutex
	nodes    map[discover.NodeID]*SimNode
	services map[string]ServiceFunc
}

func NewSimAdapter(services map[string]ServiceFunc) *SimAdapter {
	return &SimAdapter{
		pipe:     pipes.NetPipe,
		nodes:    make(map[discover.NodeID]*SimNode),
		services: services,
	}
}

func NewTCPAdapter(services map[string]ServiceFunc) *SimAdapter {
	return &SimAdapter{
		pipe:     pipes.TCPPipe,
		nodes:    make(map[discover.NodeID]*SimNode),
		services: services,
	}
}

func (s *SimAdapter) Name() string {
	return "sim-adapter"
}

func (s *SimAdapter) NewNode(config *NodeConfig) (Node, error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	// check a node with the ID doesn't already exist
	id := config.ID
	if _, exists := s.nodes[id]; exists {
		return nil, fmt.Errorf("node already exists: %s", id)
	}

	// check the services are valid
	if len(config.Services) == 0 {
		return nil, errors.New("node must have at least one service")
	}
	for _, service := range config.Services {
		if _, exists := s.services[service]; !exists {
			return nil, fmt.Errorf("unknown node service %q", service)
		}
	}

	n, err := node.New(&node.Config{
		P2P: p2p.Config{
			PrivateKey:      config.PrivateKey,
			MaxPeers:        math.MaxInt32,
			NoDiscovery:     true,
			Dialer:          s,
			EnableMsgEvents: config.EnableMsgEvents,
		},
		NoUSB:  true,
		Logger: log4j.New("node.id", id.String()),
	})
	if err != nil {
		return nil, err
	}

	simNode := &SimNode{
		ID:      id,
		config:  config,
		node:    n,
		adapter: s,
		running: make(map[string]node.Service),
	}
	s.nodes[id] = simNode
	return simNode, nil
}

func (s *SimAdapter) Dial(dest *discover.Node) (conn net.Conn, err error) {
	node, ok := s.GetNode(dest.ID)
	if !ok {
		return nil, fmt.Errorf("unknown node: %s", dest.ID)
	}
	srv := node.Server()
	if srv == nil {
		return nil, fmt.Errorf("node not running: %s", dest.ID)
	}
	// SimAdapter.pipe is net.Pipe (NewSimAdapter)
	pipe1, pipe2, err := s.pipe()
	if err != nil {
		return nil, err
	}
	// this is simulated 'listening'
	// asynchronously call the dialed destintion node's p2p server
	// to set up connection on the 'listening' side
	go srv.SetupConn(pipe1, 0, nil)
	return pipe2, nil
}

func (s *SimAdapter) DialRPC(id discover.NodeID) (*rpc.Client, error) {
	node, ok := s.GetNode(id)
	if !ok {
		return nil, fmt.Errorf("unknown node: %s", id)
	}
	handler, err := node.node.RPCHandler()
	if err != nil {
		return nil, err
	}
	return rpc.DialInProc(handler), nil
}

func (s *SimAdapter) GetNode(id discover.NodeID) (*SimNode, bool) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	node, ok := s.nodes[id]
	return node, ok
}

type SimNode struct {
	lock         sync.RWMutex
	ID           discover.NodeID
	config       *NodeConfig
	adapter      *SimAdapter
	node         *node.Node
	running      map[string]node.Service
	client       *rpc.Client
	registerOnce sync.Once
}

func (sn *SimNode) Addr() []byte {
	return []byte(sn.Node().String())
}

func (sn *SimNode) Node() *discover.Node {
	return discover.NewNode(sn.ID, net.IP{127, 0, 0, 1}, 30303, 30303)
}

func (sn *SimNode) Client() (*rpc.Client, error) {
	sn.lock.RLock()
	defer sn.lock.RUnlock()
	if sn.client == nil {
		return nil, errors.New("node not started")
	}
	return sn.client, nil
}

func (sn *SimNode) ServeRPC(conn net.Conn) error {
	handler, err := sn.node.RPCHandler()
	if err != nil {
		return err
	}
	handler.ServeCodec(rpc.NewJSONCodec(conn), rpc.OptionMethodInvocation|rpc.OptionSubscriptions)
	return nil
}

func (sn *SimNode) Snapshots() (map[string][]byte, error) {
	sn.lock.RLock()
	services := make(map[string]node.Service, len(sn.running))
	for name, service := range sn.running {
		services[name] = service
	}
	sn.lock.RUnlock()
	if len(services) == 0 {
		return nil, errors.New("no running services")
	}
	snapshots := make(map[string][]byte)
	for name, service := range services {
		if s, ok := service.(interface {
			Snapshot() ([]byte, error)
		}); ok {
			snap, err := s.Snapshot()
			if err != nil {
				return nil, err
			}
			snapshots[name] = snap
		}
	}
	return snapshots, nil
}

func (sn *SimNode) Start(snapshots map[string][]byte) error {
	newService := func(name string) func(ctx *node.ServiceContext) (node.Service, error) {
		return func(nodeCtx *node.ServiceContext) (node.Service, error) {
			ctx := &ServiceContext{
				RPCDialer:   sn.adapter,
				NodeContext: nodeCtx,
				Config:      sn.config,
			}
			if snapshots != nil {
				ctx.Snapshot = snapshots[name]
			}
			serviceFunc := sn.adapter.services[name]
			service, err := serviceFunc(ctx)
			if err != nil {
				return nil, err
			}
			sn.running[name] = service
			return service, nil
		}
	}

	// ensure we only register the services once in the case of the node
	// being stopped and then started again
	var regErr error
	sn.registerOnce.Do(func() {
		for _, name := range sn.config.Services {
			if err := sn.node.Register(newService(name)); err != nil {
				regErr = err
				break
			}
		}
	})
	if regErr != nil {
		return regErr
	}

	if err := sn.node.Start(); err != nil {
		return err
	}

	// create an in-process RPC client
	handler, err := sn.node.RPCHandler()
	if err != nil {
		return err
	}

	sn.lock.Lock()
	sn.client = rpc.DialInProc(handler)
	sn.lock.Unlock()

	return nil
}

func (sn *SimNode) Stop() error {
	sn.lock.Lock()
	if sn.client != nil {
		sn.client.Close()
		sn.client = nil
	}
	sn.lock.Unlock()
	return sn.node.Stop()
}

func (sn *SimNode) Services() []node.Service {
	sn.lock.RLock()
	defer sn.lock.RUnlock()
	services := make([]node.Service, 0, len(sn.running))
	for _, service := range sn.running {
		services = append(services, service)
	}
	return services
}

func (sn *SimNode) Server() *p2p.Server {
	return sn.node.Server()
}

func (sn *SimNode) SubscribeEvents(ch chan *p2p.PeerEvent) event.Subscription {
	srv := sn.Server()
	if srv == nil {
		panic("node not running")
	}
	return srv.SubscribeEvents(ch)
}

func (sn *SimNode) NodeInfo() *p2p.NodeInfo {
	server := sn.Server()
	if server == nil {
		return &p2p.NodeInfo{
			ID:    sn.ID.String(),
			Enode: sn.Node().String(),
		}
	}
	return server.NodeInfo()
}

func setSocketBuffer(conn net.Conn, socketReadBuffer int, socketWriteBuffer int) error {
	switch v := conn.(type) {
	case *net.UnixConn:
		err := v.SetReadBuffer(socketReadBuffer)
		if err != nil {
			return err
		}
		err = v.SetWriteBuffer(socketWriteBuffer)
		if err != nil {
			return err
		}
	}
	return nil
}
