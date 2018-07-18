
package network

import (
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/state"
)

/*
Hive is the logistic manager of the swarm

When the hive is started, a forever loop is launched that
asks the Overlay Topology driver (e.g., generic kademlia nodetable)
to suggest peers to bootstrap connectivity
*/

type Overlay interface {
	// suggest peers to connect to
	SuggestPeer() (OverlayAddr, int, bool)
	// register and deregister peer connections
	On(OverlayConn) (depth uint8, changed bool)
	Off(OverlayConn)
	// register peer addresses
	Register([]OverlayAddr) error
	// iterate over connected peers
	EachConn([]byte, int, func(OverlayConn, int, bool) bool)
	// iterate over known peers (address records)
	EachAddr([]byte, int, func(OverlayAddr, int, bool) bool)
	// pretty print the connectivity
	String() string
	// base Overlay address of the node itself
	BaseAddr() []byte
	// connectivity health check used for testing
	Healthy(*PeerPot) *Health
}

type HiveParams struct {
	Discovery             bool  // if want discovery of not
	PeersBroadcastSetSize uint8 // how many peers to use when relaying
	MaxPeersPerRequest    uint8 // max size for peer address batches
	KeepAliveInterval     time.Duration
}

func NewHiveParams() *HiveParams {
	return &HiveParams{
		Discovery:             true,
		PeersBroadcastSetSize: 3,
		MaxPeersPerRequest:    5,
		KeepAliveInterval:     500 * time.Millisecond,
	}
}

type Hive struct {
	*HiveParams                      // settings
	Overlay                          // the overlay connectiviy driver
	Store       state.Store          // storage interface to save peers across sessions
	addPeer     func(*discover.Node) // server callback to connect to a peer
	// bookkeeping
	lock   sync.Mutex
	ticker *time.Ticker
}

func NewHive(params *HiveParams, overlay Overlay, store state.Store) *Hive {
	return &Hive{
		HiveParams: params,
		Overlay:    overlay,
		Store:      store,
	}
}

func (h *Hive) Start(server *p2p.Server) error {
	log.Info(fmt.Sprintf("%08x hive starting", h.BaseAddr()[:4]))
	// if state store is specified, load peers to prepopulate the overlay address book
	if h.Store != nil {
		log.Info("detected an existing store. trying to load peers")
		if err := h.loadPeers(); err != nil {
			log.Error(fmt.Sprintf("%08x hive encoutered an error trying to load peers", h.BaseAddr()[:4]))
			return err
		}
	}
	// assigns the p2p.Server#AddPeer function to connect to peers
	h.addPeer = server.AddPeer
	// ticker to keep the hive alive
	h.ticker = time.NewTicker(h.KeepAliveInterval)
	// this loop is doing bootstrapping and maintains a healthy table
	go h.connect()
	return nil
}

func (h *Hive) Stop() error {
	log.Info(fmt.Sprintf("%08x hive stopping, saving peers", h.BaseAddr()[:4]))
	h.ticker.Stop()
	if h.Store != nil {
		if err := h.savePeers(); err != nil {
			return fmt.Errorf("could not save peers to persistence store: %v", err)
		}
		if err := h.Store.Close(); err != nil {
			return fmt.Errorf("could not close file handle to persistence store: %v", err)
		}
	}
	log.Info(fmt.Sprintf("%08x hive stopped, dropping peers", h.BaseAddr()[:4]))
	h.EachConn(nil, 255, func(p OverlayConn, _ int, _ bool) bool {
		log.Info(fmt.Sprintf("%08x dropping peer %08x", h.BaseAddr()[:4], p.Address()[:4]))
		p.Drop(nil)
		return true
	})

	log.Info(fmt.Sprintf("%08x all peers dropped", h.BaseAddr()[:4]))
	return nil
}

func (h *Hive) connect() {
	for range h.ticker.C {

		addr, depth, changed := h.SuggestPeer()
		if h.Discovery && changed {
			NotifyDepth(uint8(depth), h)
		}
		if addr == nil {
			continue
		}

		log.Trace(fmt.Sprintf("%08x hive connect() suggested %08x", h.BaseAddr()[:4], addr.Address()[:4]))
		under, err := discover.ParseNode(string(addr.(Addr).Under()))
		if err != nil {
			log.Warn(fmt.Sprintf("%08x unable to connect to bee %08x: invalid node URL: %v", h.BaseAddr()[:4], addr.Address()[:4], err))
			continue
		}
		log.Trace(fmt.Sprintf("%08x attempt to connect to bee %08x", h.BaseAddr()[:4], addr.Address()[:4]))
		h.addPeer(under)
	}
}

func (h *Hive) Run(p *BzzPeer) error {
	dp := newDiscovery(p, h)
	depth, changed := h.On(dp)
	// if we want discovery, advertise change of depth
	if h.Discovery {
		if changed {
			// if depth changed, send to all peers
			NotifyDepth(depth, h)
		} else {
			// otherwise just send depth to new peer
			dp.NotifyDepth(depth)
		}
	}
	NotifyPeer(p.Off(), h)
	defer h.Off(dp)
	return dp.Run(dp.HandleMsg)
}

func (h *Hive) NodeInfo() interface{} {
	return h.String()
}

func (h *Hive) PeerInfo(id discover.NodeID) interface{} {
	addr := NewAddrFromNodeID(id)
	return struct {
		OAddr hexutil.Bytes
		UAddr hexutil.Bytes
	}{
		OAddr: addr.OAddr,
		UAddr: addr.UAddr,
	}
}

func ToAddr(pa OverlayPeer) *BzzAddr {
	if addr, ok := pa.(*BzzAddr); ok {
		return addr
	}
	if p, ok := pa.(*discPeer); ok {
		return p.BzzAddr
	}
	return pa.(*BzzPeer).BzzAddr
}

func (h *Hive) loadPeers() error {
	var as []*BzzAddr
	err := h.Store.Get("peers", &as)
	if err != nil {
		if err == state.ErrNotFound {
			log.Info(fmt.Sprintf("hive %08x: no persisted peers found", h.BaseAddr()[:4]))
			return nil
		}
		return err
	}
	log.Info(fmt.Sprintf("hive %08x: peers loaded", h.BaseAddr()[:4]))

	return h.Register(toOverlayAddrs(as...))
}

func toOverlayAddrs(as ...*BzzAddr) (oas []OverlayAddr) {
	for _, a := range as {
		oas = append(oas, OverlayAddr(a))
	}
	return
}

func (h *Hive) savePeers() error {
	var peers []*BzzAddr
	h.Overlay.EachAddr(nil, 256, func(pa OverlayAddr, i int, _ bool) bool {
		if pa == nil {
			log.Warn(fmt.Sprintf("empty addr: %v", i))
			return true
		}
		apa := ToAddr(pa)
		log.Trace("saving peer", "peer", apa)
		peers = append(peers, apa)
		return true
	})
	if err := h.Store.Put("peers", peers); err != nil {
		return fmt.Errorf("could not save peers: %v", err)
	}
	return nil
}
