package simulations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/simulations/adapters"
)

var DialBanTimeout = 200 * time.Millisecond

type NetworkConfig struct {
	ID             string `json:"id"`
	DefaultService string `json:"default_service,omitempty"`
}

type Network struct {
	NetworkConfig

	Nodes   []*Node `json:"nodes"`
	nodeMap map[cnode.ID]int

	Conns   []*Conn `json:"conns"`
	connMap map[string]int

	nodeAdapter adapters.NodeAdapter
	events      event.Feed
	lock        sync.RWMutex
	quitc       chan struct{}
}

func NewNetwork(nodeAdapter adapters.NodeAdapter, conf *NetworkConfig) *Network {
	return &Network{
		NetworkConfig: *conf,
		nodeAdapter:   nodeAdapter,
		nodeMap:       make(map[cnode.ID]int),
		connMap:       make(map[string]int),
		quitc:         make(chan struct{}),
	}
}

func (net *Network) Events() *event.Feed {
	return &net.events
}

func (net *Network) NewNodeWithConfig(conf *adapters.NodeConfig) (*Node, error) {
	net.lock.Lock()
	defer net.lock.Unlock()

	if conf.Reachable == nil {
		conf.Reachable = func(otherID cnode.ID) bool {
			_, err := net.InitConn(conf.ID, otherID)
			if err != nil && bytes.Compare(conf.ID.Bytes(), otherID.Bytes()) < 0 {
				return false
			}
			return true
		}
	}

	if node := net.getNode(conf.ID); node != nil {
		return nil, fmt.Errorf("node with ID %q already exists", conf.ID)
	}
	if node := net.getNodeByName(conf.Name); node != nil {
		return nil, fmt.Errorf("node with name %q already exists", conf.Name)
	}

	if len(conf.Services) == 0 {
		conf.Services = []string{net.DefaultService}
	}

	adapterNode, err := net.nodeAdapter.NewNode(conf)
	if err != nil {
		return nil, err
	}
	node := &Node{
		Node:   adapterNode,
		Config: conf,
	}
	log4j.Trace("Node created", "id", conf.ID)
	net.nodeMap[conf.ID] = len(net.Nodes)
	net.Nodes = append(net.Nodes, node)

	net.events.Send(ControlEvent(node))

	return node, nil
}

func (net *Network) Config() *NetworkConfig {
	return &net.NetworkConfig
}

func (net *Network) StartAll() error {
	for _, node := range net.Nodes {
		if node.Up {
			continue
		}
		if err := net.Start(node.ID()); err != nil {
			return err
		}
	}
	return nil
}

func (net *Network) StopAll() error {
	for _, node := range net.Nodes {
		if !node.Up {
			continue
		}
		if err := net.Stop(node.ID()); err != nil {
			return err
		}
	}
	return nil
}

func (net *Network) Start(id cnode.ID) error {
	return net.startWithSnapshots(id, nil)
}

func (net *Network) startWithSnapshots(id cnode.ID, snapshots map[string][]byte) error {
	net.lock.Lock()
	defer net.lock.Unlock()

	node := net.getNode(id)
	if node == nil {
		return fmt.Errorf("node %v does not exist", id)
	}
	if node.Up {
		return fmt.Errorf("node %v already up", id)
	}
	log4j.Trace("Starting node", "id", id, "adapter", net.nodeAdapter.Name())
	if err := node.Start(snapshots); err != nil {
		log4j.Warn("Node startup failed", "id", id, "err", err)
		return err
	}
	node.Up = true
	log4j.Info("Started node", "id", id)

	net.events.Send(NewEvent(node))

	client, err := node.Client()
	if err != nil {
		return fmt.Errorf("error getting rpc client  for node %v: %s", id, err)
	}
	events := make(chan *p2p.PeerEvent)
	sub, err := client.Subscribe(context.Background(), "admin", events, "peerEvents")
	if err != nil {
		return fmt.Errorf("error getting peer events for node %v: %s", id, err)
	}
	go net.watchPeerEvents(id, events, sub)
	return nil
}

func (net *Network) watchPeerEvents(id cnode.ID, events chan *p2p.PeerEvent, sub event.Subscription) {
	defer func() {
		sub.Unsubscribe()

		net.lock.Lock()
		defer net.lock.Unlock()
		node := net.getNode(id)
		if node == nil {
			return
		}
		node.Up = false
		net.events.Send(NewEvent(node))
	}()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			peer := event.Peer
			switch event.Type {

			case p2p.PeerEventTypeAdd:
				net.DidConnect(id, peer)

			case p2p.PeerEventTypeDrop:
				net.DidDisconnect(id, peer)

			case p2p.PeerEventTypeMsgSend:
				net.DidSend(id, peer, event.Protocol, *event.MsgCode)

			case p2p.PeerEventTypeMsgRecv:
				net.DidReceive(peer, id, event.Protocol, *event.MsgCode)

			}

		case err := <-sub.Err():
			if err != nil {
				log4j.Error("Error in peer event subscription", "id", id, "err", err)
			}
			return
		}
	}
}

func (net *Network) Stop(id cnode.ID) error {
	net.lock.Lock()
	node := net.getNode(id)
	if node == nil {
		return fmt.Errorf("node %v does not exist", id)
	}
	if !node.Up {
		return fmt.Errorf("node %v already down", id)
	}
	node.Up = false
	net.lock.Unlock()

	err := node.Stop()
	if err != nil {
		net.lock.Lock()
		node.Up = true
		net.lock.Unlock()
		return err
	}
	log4j.Info("Stopped node", "id", id, "err", err)
	net.events.Send(ControlEvent(node))
	return nil
}

func (net *Network) Connect(oneID, otherID cnode.ID) error {
	log4j.Debug("Connecting nodes with addPeer", "id", oneID, "other", otherID)
	conn, err := net.InitConn(oneID, otherID)
	if err != nil {
		return err
	}
	client, err := conn.one.Client()
	if err != nil {
		return err
	}
	net.events.Send(ControlEvent(conn))
	return client.Call(nil, "admin_addPeer", string(conn.other.Addr()))
}

func (net *Network) Disconnect(oneID, otherID cnode.ID) error {
	conn := net.GetConn(oneID, otherID)
	if conn == nil {
		return fmt.Errorf("connection between %v and %v does not exist", oneID, otherID)
	}
	if !conn.Up {
		return fmt.Errorf("%v and %v already disconnected", oneID, otherID)
	}
	client, err := conn.one.Client()
	if err != nil {
		return err
	}
	net.events.Send(ControlEvent(conn))
	return client.Call(nil, "admin_removePeer", string(conn.other.Addr()))
}

func (net *Network) DidConnect(one, other cnode.ID) error {
	net.lock.Lock()
	defer net.lock.Unlock()
	conn, err := net.getOrCreateConn(one, other)
	if err != nil {
		return fmt.Errorf("connection between %v and %v does not exist", one, other)
	}
	if conn.Up {
		return fmt.Errorf("%v and %v already connected", one, other)
	}
	conn.Up = true
	net.events.Send(NewEvent(conn))
	return nil
}

func (net *Network) DidDisconnect(one, other cnode.ID) error {
	net.lock.Lock()
	defer net.lock.Unlock()
	conn := net.getConn(one, other)
	if conn == nil {
		return fmt.Errorf("connection between %v and %v does not exist", one, other)
	}
	if !conn.Up {
		return fmt.Errorf("%v and %v already disconnected", one, other)
	}
	conn.Up = false
	conn.initiated = time.Now().Add(-DialBanTimeout)
	net.events.Send(NewEvent(conn))
	return nil
}

func (net *Network) DidSend(sender, receiver cnode.ID, proto string, code uint64) error {
	msg := &Msg{
		One:      sender,
		Other:    receiver,
		Protocol: proto,
		Code:     code,
		Received: false,
	}
	net.events.Send(NewEvent(msg))
	return nil
}

func (net *Network) DidReceive(sender, receiver cnode.ID, proto string, code uint64) error {
	msg := &Msg{
		One:      sender,
		Other:    receiver,
		Protocol: proto,
		Code:     code,
		Received: true,
	}
	net.events.Send(NewEvent(msg))
	return nil
}

func (net *Network) GetNode(id cnode.ID) *Node {
	net.lock.Lock()
	defer net.lock.Unlock()
	return net.getNode(id)
}

func (net *Network) GetNodeByName(name string) *Node {
	net.lock.Lock()
	defer net.lock.Unlock()
	return net.getNodeByName(name)
}

func (net *Network) GetNodes() (nodes []*Node) {
	net.lock.Lock()
	defer net.lock.Unlock()

	nodes = append(nodes, net.Nodes...)
	return nodes
}

func (net *Network) getNode(id cnode.ID) *Node {
	i, found := net.nodeMap[id]
	if !found {
		return nil
	}
	return net.Nodes[i]
}

func (net *Network) getNodeByName(name string) *Node {
	for _, node := range net.Nodes {
		if node.Config.Name == name {
			return node
		}
	}
	return nil
}

func (net *Network) GetConn(oneID, otherID cnode.ID) *Conn {
	net.lock.Lock()
	defer net.lock.Unlock()
	return net.getConn(oneID, otherID)
}

func (net *Network) GetOrCreateConn(oneID, otherID cnode.ID) (*Conn, error) {
	net.lock.Lock()
	defer net.lock.Unlock()
	return net.getOrCreateConn(oneID, otherID)
}

func (net *Network) getOrCreateConn(oneID, otherID cnode.ID) (*Conn, error) {
	if conn := net.getConn(oneID, otherID); conn != nil {
		return conn, nil
	}

	one := net.getNode(oneID)
	if one == nil {
		return nil, fmt.Errorf("node %v does not exist", oneID)
	}
	other := net.getNode(otherID)
	if other == nil {
		return nil, fmt.Errorf("node %v does not exist", otherID)
	}
	conn := &Conn{
		One:   oneID,
		Other: otherID,
		one:   one,
		other: other,
	}
	label := ConnLabel(oneID, otherID)
	net.connMap[label] = len(net.Conns)
	net.Conns = append(net.Conns, conn)
	return conn, nil
}

func (net *Network) getConn(oneID, otherID cnode.ID) *Conn {
	label := ConnLabel(oneID, otherID)
	i, found := net.connMap[label]
	if !found {
		return nil
	}
	return net.Conns[i]
}

func (net *Network) InitConn(oneID, otherID cnode.ID) (*Conn, error) {
	net.lock.Lock()
	defer net.lock.Unlock()
	if oneID == otherID {
		return nil, fmt.Errorf("refusing to connect to self %v", oneID)
	}
	conn, err := net.getOrCreateConn(oneID, otherID)
	if err != nil {
		return nil, err
	}
	if conn.Up {
		return nil, fmt.Errorf("%v and %v already connected", oneID, otherID)
	}
	if time.Since(conn.initiated) < DialBanTimeout {
		return nil, fmt.Errorf("connection between %v and %v recently attempted", oneID, otherID)
	}

	err = conn.nodesUp()
	if err != nil {
		log4j.Trace("Nodes not up", "err", err)
		return nil, fmt.Errorf("nodes not up: %v", err)
	}
	log4j.Debug("Connection initiated", "id", oneID, "other", otherID)
	conn.initiated = time.Now()
	return conn, nil
}

func (net *Network) Shutdown() {
	for _, node := range net.Nodes {
		log4j.Debug("Stopping node", "id", node.ID())
		if err := node.Stop(); err != nil {
			log4j.Warn("Can't stop node", "id", node.ID(), "err", err)
		}
	}
	close(net.quitc)
}

func (net *Network) Reset() {
	net.lock.Lock()
	defer net.lock.Unlock()

	net.connMap = make(map[string]int)
	net.nodeMap = make(map[cnode.ID]int)

	net.Nodes = nil
	net.Conns = nil
}

type Node struct {
	adapters.Node `json:"-"`

	Config *adapters.NodeConfig `json:"config"`

	Up bool `json:"up"`
}

func (n *Node) ID() cnode.ID {
	return n.Config.ID
}

func (n *Node) String() string {
	return fmt.Sprintf("Node %v", n.ID().TerminalString())
}

func (n *Node) NodeInfo() *p2p.NodeInfo {
	if n.Node == nil {
		return nil
	}
	info := n.Node.NodeInfo()
	info.Name = n.Config.Name
	return info
}

func (n *Node) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Info   *p2p.NodeInfo        `json:"info,omitempty"`
		Config *adapters.NodeConfig `json:"config,omitempty"`
		Up     bool                 `json:"up"`
	}{
		Info:   n.NodeInfo(),
		Config: n.Config,
		Up:     n.Up,
	})
}

type Conn struct {
	One cnode.ID `json:"one"`

	Other cnode.ID `json:"other"`

	Up        bool `json:"up"`
	initiated time.Time

	one   *Node
	other *Node
}

func (c *Conn) nodesUp() error {
	if !c.one.Up {
		return fmt.Errorf("one %v is not up", c.One)
	}
	if !c.other.Up {
		return fmt.Errorf("other %v is not up", c.Other)
	}
	return nil
}

func (c *Conn) String() string {
	return fmt.Sprintf("Conn %v->%v", c.One.TerminalString(), c.Other.TerminalString())
}

type Msg struct {
	One      cnode.ID `json:"one"`
	Other    cnode.ID `json:"other"`
	Protocol string   `json:"protocol"`
	Code     uint64   `json:"code"`
	Received bool     `json:"received"`
}

func (m *Msg) String() string {
	return fmt.Sprintf("Msg(%d) %v->%v", m.Code, m.One.TerminalString(), m.Other.TerminalString())
}

func ConnLabel(source, target cnode.ID) string {
	var first, second cnode.ID
	if bytes.Compare(source.Bytes(), target.Bytes()) > 0 {
		first = target
		second = source
	} else {
		first = source
		second = target
	}
	return fmt.Sprintf("%v-%v", first, second)
}

type Snapshot struct {
	Nodes []NodeSnapshot `json:"nodes,omitempty"`
	Conns []Conn         `json:"conns,omitempty"`
}

type NodeSnapshot struct {
	Node Node `json:"node,omitempty"`

	Snapshots map[string][]byte `json:"snapshots,omitempty"`
}

func (net *Network) Snapshot() (*Snapshot, error) {
	net.lock.Lock()
	defer net.lock.Unlock()
	snap := &Snapshot{
		Nodes: make([]NodeSnapshot, len(net.Nodes)),
		Conns: make([]Conn, len(net.Conns)),
	}
	for i, node := range net.Nodes {
		snap.Nodes[i] = NodeSnapshot{Node: *node}
		if !node.Up {
			continue
		}
		snapshots, err := node.Snapshots()
		if err != nil {
			return nil, err
		}
		snap.Nodes[i].Snapshots = snapshots
	}
	for i, conn := range net.Conns {
		snap.Conns[i] = *conn
	}
	return snap, nil
}

func (net *Network) Load(snap *Snapshot) error {
	for _, n := range snap.Nodes {
		if _, err := net.NewNodeWithConfig(n.Node.Config); err != nil {
			return err
		}
		if !n.Node.Up {
			continue
		}
		if err := net.startWithSnapshots(n.Node.Config.ID, n.Snapshots); err != nil {
			return err
		}
	}
	for _, conn := range snap.Conns {

		if !net.GetNode(conn.One).Up || !net.GetNode(conn.Other).Up {
			continue
		}
		if err := net.Connect(conn.One, conn.Other); err != nil {
			return err
		}
	}
	return nil
}

func (net *Network) Subscribe(events chan *Event) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Control {
				net.executeControlEvent(event)
			}
		case <-net.quitc:
			return
		}
	}
}

func (net *Network) executeControlEvent(event *Event) {
	log4j.Trace("Executing control event", "type", event.Type, "event", event)
	switch event.Type {
	case EventTypeNode:
		if err := net.executeNodeEvent(event); err != nil {
			log4j.Error("Error executing node event", "event", event, "err", err)
		}
	case EventTypeConn:
		if err := net.executeConnEvent(event); err != nil {
			log4j.Error("Error executing conn event", "event", event, "err", err)
		}
	case EventTypeMsg:
		log4j.Warn("Ignoring control msg event")
	}
}

func (net *Network) executeNodeEvent(e *Event) error {
	if !e.Node.Up {
		return net.Stop(e.Node.ID())
	}

	if _, err := net.NewNodeWithConfig(e.Node.Config); err != nil {
		return err
	}
	return net.Start(e.Node.ID())
}

func (net *Network) executeConnEvent(e *Event) error {
	if e.Conn.Up {
		return net.Connect(e.Conn.One, e.Conn.Other)
	} else {
		return net.Disconnect(e.Conn.One, e.Conn.Other)
	}
}
