package p2p

import (
	"net"

	"github.com/5uwifi/canchain/lib/metrics"
)

var (
	ingressConnectMeter = metrics.NewRegisteredMeter("p2p/InboundConnects", nil)
	ingressTrafficMeter = metrics.NewRegisteredMeter("p2p/InboundTraffic", nil)
	egressConnectMeter  = metrics.NewRegisteredMeter("p2p/OutboundConnects", nil)
	egressTrafficMeter  = metrics.NewRegisteredMeter("p2p/OutboundTraffic", nil)
)

type meteredConn struct {
	net.Conn
}

func newMeteredConn(conn net.Conn, ingress bool) net.Conn {
	if !metrics.Enabled {
		return conn
	}
	if ingress {
		ingressConnectMeter.Mark(1)
	} else {
		egressConnectMeter.Mark(1)
	}
	return &meteredConn{Conn: conn}
}

func (c *meteredConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	ingressTrafficMeter.Mark(int64(n))
	return
}

func (c *meteredConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	egressTrafficMeter.Mark(int64(n))
	return
}
