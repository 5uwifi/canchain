
package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/5uwifi/canchain/basis/crypto"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/p2p/protocols"
	"github.com/5uwifi/canchain/rpc"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/state"
)

const (
	DefaultNetworkID = 3
	// ProtocolMaxMsgSize maximum allowed message size
	ProtocolMaxMsgSize = 10 * 1024 * 1024
	// timeout for waiting
	bzzHandshakeTimeout = 3000 * time.Millisecond
)

var BzzSpec = &protocols.Spec{
	Name:       "bzz",
	Version:    4,
	MaxMsgSize: 10 * 1024 * 1024,
	Messages: []interface{}{
		HandshakeMsg{},
	},
}

var DiscoverySpec = &protocols.Spec{
	Name:       "hive",
	Version:    4,
	MaxMsgSize: 10 * 1024 * 1024,
	Messages: []interface{}{
		peersMsg{},
		subPeersMsg{},
	},
}

type Addr interface {
	OverlayPeer
	Over() []byte
	Under() []byte
	String() string
	Update(OverlayAddr) OverlayAddr
}

type Peer interface {
	Addr                   // the address of a peer
	Conn                   // the live connection (protocols.Peer)
	LastActive() time.Time // last time active
}

type Conn interface {
	ID() discover.NodeID                                                                  // the key that uniquely identifies the Node for the peerPool
	Handshake(context.Context, interface{}, func(interface{}) error) (interface{}, error) // can send messages
	Send(interface{}) error                                                               // can send messages
	Drop(error)                                                                           // disconnect this peer
	Run(func(interface{}) error) error                                                    // the run function to run a protocol
	Off() OverlayAddr
}

type BzzConfig struct {
	OverlayAddr  []byte // base address of the overlay network
	UnderlayAddr []byte // node's underlay address
	HiveParams   *HiveParams
	NetworkID    uint64
}

type Bzz struct {
	*Hive
	NetworkID    uint64
	localAddr    *BzzAddr
	mtx          sync.Mutex
	handshakes   map[discover.NodeID]*HandshakeMsg
	streamerSpec *protocols.Spec
	streamerRun  func(*BzzPeer) error
}

// * bzz config
// * overlay driver
// * peer store
func NewBzz(config *BzzConfig, kad Overlay, store state.Store, streamerSpec *protocols.Spec, streamerRun func(*BzzPeer) error) *Bzz {
	return &Bzz{
		Hive:         NewHive(config.HiveParams, kad, store),
		NetworkID:    config.NetworkID,
		localAddr:    &BzzAddr{config.OverlayAddr, config.UnderlayAddr},
		handshakes:   make(map[discover.NodeID]*HandshakeMsg),
		streamerRun:  streamerRun,
		streamerSpec: streamerSpec,
	}
}

func (b *Bzz) UpdateLocalAddr(byteaddr []byte) *BzzAddr {
	b.localAddr = b.localAddr.Update(&BzzAddr{
		UAddr: byteaddr,
		OAddr: b.localAddr.OAddr,
	}).(*BzzAddr)
	return b.localAddr
}

func (b *Bzz) NodeInfo() interface{} {
	return b.localAddr.Address()
}

// * handshake/hive
// * discovery
func (b *Bzz) Protocols() []p2p.Protocol {
	protocol := []p2p.Protocol{
		{
			Name:     BzzSpec.Name,
			Version:  BzzSpec.Version,
			Length:   BzzSpec.Length(),
			Run:      b.runBzz,
			NodeInfo: b.NodeInfo,
		},
		{
			Name:     DiscoverySpec.Name,
			Version:  DiscoverySpec.Version,
			Length:   DiscoverySpec.Length(),
			Run:      b.RunProtocol(DiscoverySpec, b.Hive.Run),
			NodeInfo: b.Hive.NodeInfo,
			PeerInfo: b.Hive.PeerInfo,
		},
	}
	if b.streamerSpec != nil && b.streamerRun != nil {
		protocol = append(protocol, p2p.Protocol{
			Name:    b.streamerSpec.Name,
			Version: b.streamerSpec.Version,
			Length:  b.streamerSpec.Length(),
			Run:     b.RunProtocol(b.streamerSpec, b.streamerRun),
		})
	}
	return protocol
}

// * hive
func (b *Bzz) APIs() []rpc.API {
	return []rpc.API{{
		Namespace: "hive",
		Version:   "3.0",
		Service:   b.Hive,
	}}
}

// * p2p protocol spec
// * run function taking BzzPeer as argument
func (b *Bzz) RunProtocol(spec *protocols.Spec, run func(*BzzPeer) error) func(*p2p.Peer, p2p.MsgReadWriter) error {
	return func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
		// wait for the bzz protocol to perform the handshake
		handshake, _ := b.GetHandshake(p.ID())
		defer b.removeHandshake(p.ID())
		select {
		case <-handshake.done:
		case <-time.After(bzzHandshakeTimeout):
			return fmt.Errorf("%08x: %s protocol timeout waiting for handshake on %08x", b.BaseAddr()[:4], spec.Name, p.ID().Bytes()[:4])
		}
		if handshake.err != nil {
			return fmt.Errorf("%08x: %s protocol closed: %v", b.BaseAddr()[:4], spec.Name, handshake.err)
		}
		// the handshake has succeeded so construct the BzzPeer and run the protocol
		peer := &BzzPeer{
			Peer:       protocols.NewPeer(p, rw, spec),
			localAddr:  b.localAddr,
			BzzAddr:    handshake.peerAddr,
			lastActive: time.Now(),
		}
		return run(peer)
	}
}

func (b *Bzz) performHandshake(p *protocols.Peer, handshake *HandshakeMsg) error {
	ctx, cancel := context.WithTimeout(context.Background(), bzzHandshakeTimeout)
	defer func() {
		close(handshake.done)
		cancel()
	}()
	rsh, err := p.Handshake(ctx, handshake, b.checkHandshake)
	if err != nil {
		handshake.err = err
		return err
	}
	handshake.peerAddr = rsh.(*HandshakeMsg).Addr
	return nil
}

func (b *Bzz) runBzz(p *p2p.Peer, rw p2p.MsgReadWriter) error {
	handshake, _ := b.GetHandshake(p.ID())
	if !<-handshake.init {
		return fmt.Errorf("%08x: bzz already started on peer %08x", b.localAddr.Over()[:4], ToOverlayAddr(p.ID().Bytes())[:4])
	}
	close(handshake.init)
	defer b.removeHandshake(p.ID())
	peer := protocols.NewPeer(p, rw, BzzSpec)
	err := b.performHandshake(peer, handshake)
	if err != nil {
		log.Warn(fmt.Sprintf("%08x: handshake failed with remote peer %08x: %v", b.localAddr.Over()[:4], ToOverlayAddr(p.ID().Bytes())[:4], err))

		return err
	}
	// fail if we get another handshake
	msg, err := rw.ReadMsg()
	if err != nil {
		return err
	}
	msg.Discard()
	return errors.New("received multiple handshakes")
}

type BzzPeer struct {
	*protocols.Peer           // represents the connection for online peers
	localAddr       *BzzAddr  // local Peers address
	*BzzAddr                  // remote address -> implements Addr interface = protocols.Peer
	lastActive      time.Time // time is updated whenever mutexes are releasing
}

func NewBzzTestPeer(p *protocols.Peer, addr *BzzAddr) *BzzPeer {
	return &BzzPeer{
		Peer:      p,
		localAddr: addr,
		BzzAddr:   NewAddrFromNodeID(p.ID()),
	}
}

func (p *BzzPeer) Off() OverlayAddr {
	return p.BzzAddr
}

func (p *BzzPeer) LastActive() time.Time {
	return p.lastActive
}

/*
 Handshake

* Version: 8 byte integer version of the protocol
* NetworkID: 8 byte integer network identifier
* Addr: the address advertised by the node including underlay and overlay connecctions
*/
type HandshakeMsg struct {
	Version   uint64
	NetworkID uint64
	Addr      *BzzAddr

	// peerAddr is the address received in the peer handshake
	peerAddr *BzzAddr

	init chan bool
	done chan struct{}
	err  error
}

func (bh *HandshakeMsg) String() string {
	return fmt.Sprintf("Handshake: Version: %v, NetworkID: %v, Addr: %v", bh.Version, bh.NetworkID, bh.Addr)
}

func (b *Bzz) checkHandshake(hs interface{}) error {
	rhs := hs.(*HandshakeMsg)
	if rhs.NetworkID != b.NetworkID {
		return fmt.Errorf("network id mismatch %d (!= %d)", rhs.NetworkID, b.NetworkID)
	}
	if rhs.Version != uint64(BzzSpec.Version) {
		return fmt.Errorf("version mismatch %d (!= %d)", rhs.Version, BzzSpec.Version)
	}
	return nil
}

func (b *Bzz) removeHandshake(peerID discover.NodeID) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	delete(b.handshakes, peerID)
}

func (b *Bzz) GetHandshake(peerID discover.NodeID) (*HandshakeMsg, bool) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	handshake, found := b.handshakes[peerID]
	if !found {
		handshake = &HandshakeMsg{
			Version:   uint64(BzzSpec.Version),
			NetworkID: b.NetworkID,
			Addr:      b.localAddr,
			init:      make(chan bool, 1),
			done:      make(chan struct{}),
		}
		// when handhsake is first created for a remote peer
		// it is initialised with the init
		handshake.init <- true
		b.handshakes[peerID] = handshake
	}

	return handshake, found
}

type BzzAddr struct {
	OAddr []byte
	UAddr []byte
}

func (a *BzzAddr) Address() []byte {
	return a.OAddr
}

func (a *BzzAddr) Over() []byte {
	return a.OAddr
}

func (a *BzzAddr) Under() []byte {
	return a.UAddr
}

func (a *BzzAddr) ID() discover.NodeID {
	return discover.MustParseNode(string(a.UAddr)).ID
}

func (a *BzzAddr) Update(na OverlayAddr) OverlayAddr {
	return &BzzAddr{a.OAddr, na.(Addr).Under()}
}

func (a *BzzAddr) String() string {
	return fmt.Sprintf("%x <%s>", a.OAddr, a.UAddr)
}

func RandomAddr() *BzzAddr {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic("unable to generate key")
	}
	pubkey := crypto.FromECDSAPub(&key.PublicKey)
	var id discover.NodeID
	copy(id[:], pubkey[1:])
	return NewAddrFromNodeID(id)
}

func NewNodeIDFromAddr(addr Addr) discover.NodeID {
	log.Info(fmt.Sprintf("uaddr=%s", string(addr.Under())))
	node := discover.MustParseNode(string(addr.Under()))
	return node.ID
}

func NewAddrFromNodeID(id discover.NodeID) *BzzAddr {
	return &BzzAddr{
		OAddr: ToOverlayAddr(id.Bytes()),
		UAddr: []byte(discover.NewNode(id, net.IP{127, 0, 0, 1}, 30303, 30303).String()),
	}
}

func NewAddrFromNodeIDAndPort(id discover.NodeID, host net.IP, port uint16) *BzzAddr {
	return &BzzAddr{
		OAddr: ToOverlayAddr(id.Bytes()),
		UAddr: []byte(discover.NewNode(id, host, port, port).String()),
	}
}

func ToOverlayAddr(id []byte) []byte {
	return crypto.Keccak256(id)
}
