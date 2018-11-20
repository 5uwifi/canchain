package p2p

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5uwifi/canchain/lib/p2p/cnode"

	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/metrics"
)

const (
	MetricsInboundConnects  = "p2p/InboundConnects"
	MetricsInboundTraffic   = "p2p/InboundTraffic"
	MetricsOutboundConnects = "p2p/OutboundConnects"
	MetricsOutboundTraffic  = "p2p/OutboundTraffic"

	MeteredPeerLimit = 1024
)

var (
	ingressConnectMeter = metrics.NewRegisteredMeter(MetricsInboundConnects, nil)
	ingressTrafficMeter = metrics.NewRegisteredMeter(MetricsInboundTraffic, nil)
	egressConnectMeter  = metrics.NewRegisteredMeter(MetricsOutboundConnects, nil)
	egressTrafficMeter  = metrics.NewRegisteredMeter(MetricsOutboundTraffic, nil)

	PeerIngressRegistry = metrics.NewPrefixedChildRegistry(metrics.EphemeralRegistry, MetricsInboundTraffic+"/")
	PeerEgressRegistry  = metrics.NewPrefixedChildRegistry(metrics.EphemeralRegistry, MetricsOutboundTraffic+"/")

	meteredPeerFeed  event.Feed
	meteredPeerCount int32
)

type MeteredPeerEventType int

const (
	PeerConnected MeteredPeerEventType = iota

	PeerDisconnected

	PeerHandshakeFailed
)

type MeteredPeerEvent struct {
	Type    MeteredPeerEventType
	IP      net.IP
	ID      cnode.ID
	Elapsed time.Duration
	Ingress uint64
	Egress  uint64
}

func SubscribeMeteredPeerEvent(ch chan<- MeteredPeerEvent) event.Subscription {
	return meteredPeerFeed.Subscribe(ch)
}

type meteredConn struct {
	net.Conn

	connected time.Time
	ip        net.IP
	id        cnode.ID

	trafficMetered bool
	ingressMeter   metrics.Meter
	egressMeter    metrics.Meter

	lock sync.RWMutex
}

func newMeteredConn(conn net.Conn, ingress bool, ip net.IP) net.Conn {
	if !metrics.Enabled {
		return conn
	}
	if ip.IsUnspecified() {
		log4j.Warn("Peer IP is unspecified")
		return conn
	}
	if ingress {
		ingressConnectMeter.Mark(1)
	} else {
		egressConnectMeter.Mark(1)
	}
	return &meteredConn{
		Conn:      conn,
		ip:        ip,
		connected: time.Now(),
	}
}

func (c *meteredConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	ingressTrafficMeter.Mark(int64(n))
	c.lock.RLock()
	if c.trafficMetered {
		c.ingressMeter.Mark(int64(n))
	}
	c.lock.RUnlock()
	return n, err
}

func (c *meteredConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	egressTrafficMeter.Mark(int64(n))
	c.lock.RLock()
	if c.trafficMetered {
		c.egressMeter.Mark(int64(n))
	}
	c.lock.RUnlock()
	return n, err
}

func (c *meteredConn) handshakeDone(id cnode.ID) {
	if atomic.AddInt32(&meteredPeerCount, 1) >= MeteredPeerLimit {
		atomic.AddInt32(&meteredPeerCount, -1)
		c.lock.Lock()
		c.id, c.trafficMetered = id, false
		c.lock.Unlock()
		log4j.Warn("Metered peer count reached the limit")
	} else {
		key := fmt.Sprintf("%s/%s", c.ip, id.String())
		c.lock.Lock()
		c.id, c.trafficMetered = id, true
		c.ingressMeter = metrics.NewRegisteredMeter(key, PeerIngressRegistry)
		c.egressMeter = metrics.NewRegisteredMeter(key, PeerEgressRegistry)
		c.lock.Unlock()
	}
	meteredPeerFeed.Send(MeteredPeerEvent{
		Type:    PeerConnected,
		IP:      c.ip,
		ID:      id,
		Elapsed: time.Since(c.connected),
	})
}

func (c *meteredConn) Close() error {
	err := c.Conn.Close()
	c.lock.RLock()
	if c.id == (cnode.ID{}) {
		c.lock.RUnlock()
		meteredPeerFeed.Send(MeteredPeerEvent{
			Type:    PeerHandshakeFailed,
			IP:      c.ip,
			Elapsed: time.Since(c.connected),
		})
		return err
	}
	id := c.id
	if !c.trafficMetered {
		c.lock.RUnlock()
		meteredPeerFeed.Send(MeteredPeerEvent{
			Type: PeerDisconnected,
			IP:   c.ip,
			ID:   id,
		})
		return err
	}
	ingress, egress := uint64(c.ingressMeter.Count()), uint64(c.egressMeter.Count())
	c.lock.RUnlock()

	atomic.AddInt32(&meteredPeerCount, -1)

	key := fmt.Sprintf("%s/%s", c.ip, id)
	PeerIngressRegistry.Unregister(key)
	PeerEgressRegistry.Unregister(key)

	meteredPeerFeed.Send(MeteredPeerEvent{
		Type:    PeerDisconnected,
		IP:      c.ip,
		ID:      id,
		Ingress: ingress,
		Egress:  egress,
	})
	return err
}
