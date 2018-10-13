package cnode

import (
	"crypto/ecdsa"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p/enr"
	"github.com/5uwifi/canchain/lib/p2p/netutil"
)

const (
	iptrackMinStatements = 10
	iptrackWindow        = 5 * time.Minute
	iptrackContactWindow = 10 * time.Minute
)

type LocalNode struct {
	cur atomic.Value
	id  ID
	key *ecdsa.PrivateKey
	db  *DB

	mu          sync.Mutex
	seq         uint64
	entries     map[string]enr.Entry
	udpTrack    *netutil.IPTracker
	staticIP    net.IP
	fallbackIP  net.IP
	fallbackUDP int
}

func NewLocalNode(db *DB, key *ecdsa.PrivateKey) *LocalNode {
	ln := &LocalNode{
		id:       PubkeyToIDV4(&key.PublicKey),
		db:       db,
		key:      key,
		udpTrack: netutil.NewIPTracker(iptrackWindow, iptrackContactWindow, iptrackMinStatements),
		entries:  make(map[string]enr.Entry),
	}
	ln.seq = db.localSeq(ln.id)
	ln.invalidate()
	return ln
}

func (ln *LocalNode) Database() *DB {
	return ln.db
}

func (ln *LocalNode) Node() *Node {
	n := ln.cur.Load().(*Node)
	if n != nil {
		return n
	}
	ln.mu.Lock()
	defer ln.mu.Unlock()
	ln.sign()
	return ln.cur.Load().(*Node)
}

func (ln *LocalNode) ID() ID {
	return ln.id
}

func (ln *LocalNode) Set(e enr.Entry) {
	ln.mu.Lock()
	defer ln.mu.Unlock()

	ln.set(e)
}

func (ln *LocalNode) set(e enr.Entry) {
	val, exists := ln.entries[e.ENRKey()]
	if !exists || !reflect.DeepEqual(val, e) {
		ln.entries[e.ENRKey()] = e
		ln.invalidate()
	}
}

func (ln *LocalNode) Delete(e enr.Entry) {
	ln.mu.Lock()
	defer ln.mu.Unlock()

	ln.delete(e)
}

func (ln *LocalNode) delete(e enr.Entry) {
	_, exists := ln.entries[e.ENRKey()]
	if exists {
		delete(ln.entries, e.ENRKey())
		ln.invalidate()
	}
}

func (ln *LocalNode) SetStaticIP(ip net.IP) {
	ln.mu.Lock()
	defer ln.mu.Unlock()

	ln.staticIP = ip
	ln.updateEndpoints()
}

func (ln *LocalNode) SetFallbackIP(ip net.IP) {
	ln.mu.Lock()
	defer ln.mu.Unlock()

	ln.fallbackIP = ip
	ln.updateEndpoints()
}

func (ln *LocalNode) SetFallbackUDP(port int) {
	ln.mu.Lock()
	defer ln.mu.Unlock()

	ln.fallbackUDP = port
	ln.updateEndpoints()
}

func (ln *LocalNode) UDPEndpointStatement(fromaddr, endpoint *net.UDPAddr) {
	ln.mu.Lock()
	defer ln.mu.Unlock()

	ln.udpTrack.AddStatement(fromaddr.String(), endpoint.String())
	ln.updateEndpoints()
}

func (ln *LocalNode) UDPContact(toaddr *net.UDPAddr) {
	ln.mu.Lock()
	defer ln.mu.Unlock()

	ln.udpTrack.AddContact(toaddr.String())
	ln.updateEndpoints()
}

func (ln *LocalNode) updateEndpoints() {
	newIP := ln.fallbackIP
	newUDP := ln.fallbackUDP
	if ln.staticIP != nil {
		newIP = ln.staticIP
	} else if ip, port := predictAddr(ln.udpTrack); ip != nil {
		newIP = ip
		newUDP = port
	}

	if newIP != nil && !newIP.IsUnspecified() {
		ln.set(enr.IP(newIP))
		if newUDP != 0 {
			ln.set(enr.UDP(newUDP))
		} else {
			ln.delete(enr.UDP(0))
		}
	} else {
		ln.delete(enr.IP{})
	}
}

func predictAddr(t *netutil.IPTracker) (net.IP, int) {
	ep := t.PredictEndpoint()
	if ep == "" {
		return nil, 0
	}
	ipString, portString, _ := net.SplitHostPort(ep)
	ip := net.ParseIP(ipString)
	port, _ := strconv.Atoi(portString)
	return ip, port
}

func (ln *LocalNode) invalidate() {
	ln.cur.Store((*Node)(nil))
}

func (ln *LocalNode) sign() {
	if n := ln.cur.Load().(*Node); n != nil {
		return
	}

	var r enr.Record
	for _, e := range ln.entries {
		r.Set(e)
	}
	ln.bumpSeq()
	r.SetSeq(ln.seq)
	if err := SignV4(&r, ln.key); err != nil {
		panic(fmt.Errorf("ccnode: can't sign record: %v", err))
	}
	n, err := New(ValidSchemes, &r)
	if err != nil {
		panic(fmt.Errorf("ccnode: can't verify local record: %v", err))
	}
	ln.cur.Store(n)
	log4j.Info("New local node record", "seq", ln.seq, "id", n.ID(), "ip", n.IP(), "udp", n.UDP(), "tcp", n.TCP())
}

func (ln *LocalNode) bumpSeq() {
	ln.seq++
	ln.db.storeLocalSeq(ln.id, ln.seq)
}
