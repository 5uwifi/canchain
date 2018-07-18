
package simulations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain/basis/event"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/p2p/simulations/adapters"
)

var DialBanTimeout = 200 * time.Millisecond

type NetworkConfig struct {
	ID             string `json:"id"`
	DefaultService string `json:"default_service,omitempty"`
}

//
//
type Network struct {
	NetworkConfig

	Nodes   []*Node `json:"nodes"`
	nodeMap map[discover.NodeID]int

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
		nodeMap:       make(map[discover.NodeID]int),
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
		conf.Reachable = func(otherID discover.NodeID) bool {
			_, err := net.InitConn(conf.ID, otherID)
			if err != nil && bytes.Compare(conf.ID.Bytes(), otherID.Bytes()) < 0 {
				return false
			}
			return true
		}
	}

	// check the node doesn't already exist
	if node := net.getNode(conf.ID); node != nil {
		return nil, fmt.Errorf("node with ID %q already exists", conf.ID)
	}
	if node := net.getNodeByName(conf.Name); node != nil {
		return nil, fmt.Errorf("node with name %q already exists", conf.Name)
	}

	// if no services are configured, use the default service
	if len(conf.Services) == 0 {
		conf.Services = []string{net.DefaultService}
	}

	// use the NodeAdapter to create the node
	adapterNode, err := net.nodeAdapter.NewNode(conf)
	if err != nil {
		return nil, err
	}
	node := &Node{
		Node:   adapterNode,
		Config: conf,
	}
	log4j.Trace(fmt.Sprintf("node %v created", conf.ID))
	net.nodeMap[conf.ID] = len(net.Nodes)
	net.Nodes = append(net.Nodes, node)

	// emit a "control" event
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

func (net *Network) Start(id discover.NodeID) error {
	return net.startWithSnapshots(id, nil)
}

func (net *Network) startWithSnapshots(id discover.NodeID, snapshots map[string][]byte) error {
	net.lock.Lock()
	defer net.lock.Unlock()
	node := net.getNode(id)
	if node == nil {
		return fmt.Errorf("node %v does not exist", id)
	}
	if node.Up {
		return fmt.Errorf("node %v already up", id)
	}
	log4j.Trace(fmt.Sprintf("starting node %v: %v using %v", id, node.Up, net.nodeAdapter.Name()))
	if err := node.Start(snapshots); err != nil {
		log4j.Warn(fmt.Sprintf("start up failed: %v", err))
		return err
	}
	node.Up = true
	log4j.Info(fmt.Sprintf("started node %v: %v", id, node.Up))

	net.events.Send(NewEvent(node))

	// subscribe to peer events
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

func (net *Network) watchPeerEvents(id discover.NodeID, events chan *p2p.PeerEvent, sub event.Subscription) {
	defer func() {
		sub.Unsubscribe()

		// assume the node is now down
		net.lock.Lock()
		defer net.lock.Unlock()
		node := net.getNode(id)
		if node == nil {
			log4j.Error("Can not find node for id", "id", id)
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
				log4j.Error(fmt.Sprintf("error getting peer events for node %v", id), "err", err)
			}
			return
		}
	}
}

func (net *Network) Stop(id discover.NodeID) error {
	net.lock.Lock()
	defer net.lock.Unlock()
	node := net.getNode(id)
	if node == nil {
		return fmt.Errorf("node %v does not exist", id)
	}
	if !node.Up {
		return fmt.Errorf("node %v already down", id)
	}
	if err := node.Stop(); err != nil {
		return err
	}
	node.Up = false
	log4j.Info(fmt.Sprintf("stop node %v: %v", id, node.Up))

	net.events.Send(ControlEvent(node))
	return nil
}

func (net *Network) Connect(oneID, otherID discover.NodeID) error {
	log4j.Debug(fmt.Sprintf("connecting %s to %s", oneID, otherID))
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

func (net *Network) Disconnect(oneID, otherID discover.NodeID) error {
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

func (net *Network) DidConnect(one, other discover.NodeID) error {
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

// "other" node
func (net *Network) DidDisconnect(one, other discover.NodeID) error {
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

func (net *Network) DidSend(sender, receiver discover.NodeID, proto string, code uint64) error {
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

func (net *Network) DidReceive(sender, receiver discover.NodeID, proto string, code uint64) error {
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

func (net *Network) GetNode(id discover.NodeID) *Node {
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

func (net *Network) getNode(id discover.NodeID) *Node {
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

func (net *Network) GetConn(oneID, otherID discover.NodeID) *Conn {
	net.lock.Lock()
	defer net.lock.Unlock()
	return net.getConn(oneID, otherID)
}

func (net *Network) GetOrCreateConn(oneID, otherID discover.NodeID) (*Conn, error) {
	net.lock.Lock()
	defer net.lock.Unlock()
	return net.getOrCreateConn(oneID, otherID)
}

func (net *Network) getOrCreateConn(oneID, otherID discover.NodeID) (*Conn, error) {
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

func (net *Network) getConn(oneID, otherID discover.NodeID) *Conn {
	label := ConnLabel(oneID, otherID)
	i, found := net.connMap[label]
	if !found {
		return nil
	}
	return net.Conns[i]
}

func (net *Network) InitConn(oneID, otherID discover.NodeID) (*Conn, error) {
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
		log4j.Trace(fmt.Sprintf("nodes not up: %v", err))
		return nil, fmt.Errorf("nodes not up: %v", err)
	}
	log4j.Debug("InitConn - connection initiated")
	conn.initiated = time.Now()
	return conn, nil
}

func (net *Network) Shutdown() {
	for _, node := range net.Nodes {
		log4j.Debug(fmt.Sprintf("stopping node %s", node.ID().TerminalString()))
		if err := node.Stop(); err != nil {
			log4j.Warn(fmt.Sprintf("error stopping node %s", node.ID().TerminalString()), "err", err)
		}
	}
	close(net.quitc)
}

func (net *Network) Reset() {
	net.lock.Lock()
	defer net.lock.Unlock()

	//re-initialize the maps
	net.connMap = make(map[string]int)
	net.nodeMap = make(map[discover.NodeID]int)

	net.Nodes = nil
	net.Conns = nil
}

type Node struct {
	adapters.Node `json:"-"`

	// Config if the config used to created the node
	Config *adapters.NodeConfig `json:"config"`

	// Up tracks whether or not the node is running
	Up bool `json:"up"`
}

func (n *Node) ID() discover.NodeID {
	return n.Config.ID
}

func (n *Node) String() string {
	return fmt.Sprintf("Node %v", n.ID().TerminalString())
}

func (n *Node) NodeInfo() *p2p.NodeInfo {
	// avoid a panic if the node is not started yet
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
	// One is the node which initiated the connection
	One discover.NodeID `json:"one"`

	// Other is the node which the connection was made to
	Other discover.NodeID `json:"other"`

	// Up tracks whether or not the connection is active
	Up bool `json:"up"`
	// Registers when the connection was grabbed to dial
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
	One      discover.NodeID `json:"one"`
	Other    discover.NodeID `json:"other"`
	Protocol string          `json:"protocol"`
	Code     uint64          `json:"code"`
	Received bool            `json:"received"`
}

func (m *Msg) String() string {
	return fmt.Sprintf("Msg(%d) %v->%v", m.Code, m.One.TerminalString(), m.Other.TerminalString())
}

func ConnLabel(source, target discover.NodeID) string {
	var first, second discover.NodeID
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

	// Snapshots is arbitrary data gathered from calling node.Snapshots()
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
			//in this case, at least one of the nodes of a connection is not up,
			//so it would result in the snapshot `Load` to fail
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
	log4j.Trace("execute control event", "type", event.Type, "event", event)
	switch event.Type {
	case EventTypeNode:
		if err := net.executeNodeEvent(event); err != nil {
			log4j.Error("error executing node event", "event", event, "err", err)
		}
	case EventTypeConn:
		if err := net.executeConnEvent(event); err != nil {
			log4j.Error("error executing conn event", "event", event, "err", err)
		}
	case EventTypeMsg:
		log4j.Warn("ignoring control msg event")
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
