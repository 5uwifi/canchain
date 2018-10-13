package discover

import (
	"bytes"
	"container/list"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/netutil"
	"github.com/5uwifi/canchain/lib/rlp"
)

var (
	errPacketTooSmall   = errors.New("too small")
	errBadHash          = errors.New("bad hash")
	errExpired          = errors.New("expired")
	errUnsolicitedReply = errors.New("unsolicited reply")
	errUnknownNode      = errors.New("unknown node")
	errTimeout          = errors.New("RPC timeout")
	errClockWarp        = errors.New("reply deadline too far in the future")
	errClosed           = errors.New("socket closed")
)

const (
	respTimeout    = 500 * time.Millisecond
	expiration     = 20 * time.Second
	bondExpiration = 24 * time.Hour

	ntpFailureThreshold = 32
	ntpWarningCooldown  = 10 * time.Minute
	driftThreshold      = 10 * time.Second
)

const (
	pingPacket = iota + 10
	pongPacket
	findnodePacket
	neighborsPacket
)

type (
	ping struct {
		Version    uint
		From, To   rpcEndpoint
		Expiration uint64
		Rest       []rlp.RawValue `rlp:"tail"`
	}

	pong struct {
		To rpcEndpoint

		ReplyTok   []byte
		Expiration uint64
		Rest       []rlp.RawValue `rlp:"tail"`
	}

	findnode struct {
		Target     encPubkey
		Expiration uint64
		Rest       []rlp.RawValue `rlp:"tail"`
	}

	neighbors struct {
		Nodes      []rpcNode
		Expiration uint64
		Rest       []rlp.RawValue `rlp:"tail"`
	}

	rpcNode struct {
		IP  net.IP
		UDP uint16
		TCP uint16
		ID  encPubkey
	}

	rpcEndpoint struct {
		IP  net.IP
		UDP uint16
		TCP uint16
	}
)

func makeEndpoint(addr *net.UDPAddr, tcpPort uint16) rpcEndpoint {
	ip := net.IP{}
	if ip4 := addr.IP.To4(); ip4 != nil {
		ip = ip4
	} else if ip6 := addr.IP.To16(); ip6 != nil {
		ip = ip6
	}
	return rpcEndpoint{IP: ip, UDP: uint16(addr.Port), TCP: tcpPort}
}

func (t *udp) nodeFromRPC(sender *net.UDPAddr, rn rpcNode) (*node, error) {
	if rn.UDP <= 1024 {
		return nil, errors.New("low port")
	}
	if err := netutil.CheckRelayIP(sender.IP, rn.IP); err != nil {
		return nil, err
	}
	if t.netrestrict != nil && !t.netrestrict.Contains(rn.IP) {
		return nil, errors.New("not contained in netrestrict whitelist")
	}
	key, err := decodePubkey(rn.ID)
	if err != nil {
		return nil, err
	}
	n := wrapNode(cnode.NewV4(key, rn.IP, int(rn.TCP), int(rn.UDP)))
	err = n.ValidateComplete()
	return n, err
}

func nodeToRPC(n *node) rpcNode {
	var key ecdsa.PublicKey
	var ekey encPubkey
	if err := n.Load((*cnode.Secp256k1)(&key)); err == nil {
		ekey = encodePubkey(&key)
	}
	return rpcNode{ID: ekey, IP: n.IP(), UDP: uint16(n.UDP()), TCP: uint16(n.TCP())}
}

type packet interface {
	handle(t *udp, from *net.UDPAddr, fromKey encPubkey, mac []byte) error
	name() string
}

type conn interface {
	ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error)
	WriteToUDP(b []byte, addr *net.UDPAddr) (n int, err error)
	Close() error
	LocalAddr() net.Addr
}

type udp struct {
	conn        conn
	netrestrict *netutil.Netlist
	priv        *ecdsa.PrivateKey
	localNode   *cnode.LocalNode
	db          *cnode.DB
	tab         *Table
	wg          sync.WaitGroup

	addpending chan *pending
	gotreply   chan reply
	closing    chan struct{}
}

type pending struct {
	from  cnode.ID
	ptype byte

	deadline time.Time

	callback func(resp interface{}) (done bool)

	errc chan<- error
}

type reply struct {
	from    cnode.ID
	ptype   byte
	data    interface{}
	matched chan<- bool
}

type ReadPacket struct {
	Data []byte
	Addr *net.UDPAddr
}

type Config struct {
	PrivateKey *ecdsa.PrivateKey

	NetRestrict *netutil.Netlist
	Bootnodes   []*cnode.Node
	Unhandled   chan<- ReadPacket
}

func ListenUDP(c conn, ln *cnode.LocalNode, cfg Config) (*Table, error) {
	tab, _, err := newUDP(c, ln, cfg)
	if err != nil {
		return nil, err
	}
	return tab, nil
}

func newUDP(c conn, ln *cnode.LocalNode, cfg Config) (*Table, *udp, error) {
	udp := &udp{
		conn:        c,
		priv:        cfg.PrivateKey,
		netrestrict: cfg.NetRestrict,
		localNode:   ln,
		db:          ln.Database(),
		closing:     make(chan struct{}),
		gotreply:    make(chan reply),
		addpending:  make(chan *pending),
	}
	tab, err := newTable(udp, ln.Database(), cfg.Bootnodes)
	if err != nil {
		return nil, nil, err
	}
	udp.tab = tab

	udp.wg.Add(2)
	go udp.loop()
	go udp.readLoop(cfg.Unhandled)
	return udp.tab, udp, nil
}

func (t *udp) self() *cnode.Node {
	return t.localNode.Node()
}

func (t *udp) close() {
	close(t.closing)
	t.conn.Close()
	t.wg.Wait()
}

func (t *udp) ourEndpoint() rpcEndpoint {
	n := t.self()
	a := &net.UDPAddr{IP: n.IP(), Port: n.UDP()}
	return makeEndpoint(a, uint16(n.TCP()))
}

func (t *udp) ping(toid cnode.ID, toaddr *net.UDPAddr) error {
	return <-t.sendPing(toid, toaddr, nil)
}

func (t *udp) sendPing(toid cnode.ID, toaddr *net.UDPAddr, callback func()) <-chan error {
	req := &ping{
		Version:    4,
		From:       t.ourEndpoint(),
		To:         makeEndpoint(toaddr, 0),
		Expiration: uint64(time.Now().Add(expiration).Unix()),
	}
	packet, hash, err := encodePacket(t.priv, pingPacket, req)
	if err != nil {
		errc := make(chan error, 1)
		errc <- err
		return errc
	}
	errc := t.pending(toid, pongPacket, func(p interface{}) bool {
		ok := bytes.Equal(p.(*pong).ReplyTok, hash)
		if ok && callback != nil {
			callback()
		}
		return ok
	})
	t.localNode.UDPContact(toaddr)
	t.write(toaddr, req.name(), packet)
	return errc
}

func (t *udp) waitping(from cnode.ID) error {
	return <-t.pending(from, pingPacket, func(interface{}) bool { return true })
}

func (t *udp) findnode(toid cnode.ID, toaddr *net.UDPAddr, target encPubkey) ([]*node, error) {
	if time.Since(t.db.LastPingReceived(toid)) > bondExpiration {
		t.ping(toid, toaddr)
		t.waitping(toid)
	}

	nodes := make([]*node, 0, bucketSize)
	nreceived := 0
	errc := t.pending(toid, neighborsPacket, func(r interface{}) bool {
		reply := r.(*neighbors)
		for _, rn := range reply.Nodes {
			nreceived++
			n, err := t.nodeFromRPC(toaddr, rn)
			if err != nil {
				log4j.Trace("Invalid neighbor node received", "ip", rn.IP, "addr", toaddr, "err", err)
				continue
			}
			nodes = append(nodes, n)
		}
		return nreceived >= bucketSize
	})
	t.send(toaddr, findnodePacket, &findnode{
		Target:     target,
		Expiration: uint64(time.Now().Add(expiration).Unix()),
	})
	return nodes, <-errc
}

func (t *udp) pending(id cnode.ID, ptype byte, callback func(interface{}) bool) <-chan error {
	ch := make(chan error, 1)
	p := &pending{from: id, ptype: ptype, callback: callback, errc: ch}
	select {
	case t.addpending <- p:
	case <-t.closing:
		ch <- errClosed
	}
	return ch
}

func (t *udp) handleReply(from cnode.ID, ptype byte, req packet) bool {
	matched := make(chan bool, 1)
	select {
	case t.gotreply <- reply{from, ptype, req, matched}:
		return <-matched
	case <-t.closing:
		return false
	}
}

func (t *udp) loop() {
	defer t.wg.Done()

	var (
		plist        = list.New()
		timeout      = time.NewTimer(0)
		nextTimeout  *pending
		contTimeouts = 0
		ntpWarnTime  = time.Unix(0, 0)
	)
	<-timeout.C
	defer timeout.Stop()

	resetTimeout := func() {
		if plist.Front() == nil || nextTimeout == plist.Front().Value {
			return
		}
		now := time.Now()
		for el := plist.Front(); el != nil; el = el.Next() {
			nextTimeout = el.Value.(*pending)
			if dist := nextTimeout.deadline.Sub(now); dist < 2*respTimeout {
				timeout.Reset(dist)
				return
			}
			nextTimeout.errc <- errClockWarp
			plist.Remove(el)
		}
		nextTimeout = nil
		timeout.Stop()
	}

	for {
		resetTimeout()

		select {
		case <-t.closing:
			for el := plist.Front(); el != nil; el = el.Next() {
				el.Value.(*pending).errc <- errClosed
			}
			return

		case p := <-t.addpending:
			p.deadline = time.Now().Add(respTimeout)
			plist.PushBack(p)

		case r := <-t.gotreply:
			var matched bool
			for el := plist.Front(); el != nil; el = el.Next() {
				p := el.Value.(*pending)
				if p.from == r.from && p.ptype == r.ptype {
					matched = true
					if p.callback(r.data) {
						p.errc <- nil
						plist.Remove(el)
					}
					contTimeouts = 0
				}
			}
			r.matched <- matched

		case now := <-timeout.C:
			nextTimeout = nil

			for el := plist.Front(); el != nil; el = el.Next() {
				p := el.Value.(*pending)
				if now.After(p.deadline) || now.Equal(p.deadline) {
					p.errc <- errTimeout
					plist.Remove(el)
					contTimeouts++
				}
			}
			if contTimeouts > ntpFailureThreshold {
				if time.Since(ntpWarnTime) >= ntpWarningCooldown {
					ntpWarnTime = time.Now()
					go checkClockDrift()
				}
				contTimeouts = 0
			}
		}
	}
}

const (
	macSize  = 256 / 8
	sigSize  = 520 / 8
	headSize = macSize + sigSize
)

var (
	headSpace = make([]byte, headSize)

	maxNeighbors int
)

func init() {
	p := neighbors{Expiration: ^uint64(0)}
	maxSizeNode := rpcNode{IP: make(net.IP, 16), UDP: ^uint16(0), TCP: ^uint16(0)}
	for n := 0; ; n++ {
		p.Nodes = append(p.Nodes, maxSizeNode)
		size, _, err := rlp.EncodeToReader(p)
		if err != nil {
			panic("cannot encode: " + err.Error())
		}
		if headSize+size+1 >= 1280 {
			maxNeighbors = n
			break
		}
	}
}

func (t *udp) send(toaddr *net.UDPAddr, ptype byte, req packet) ([]byte, error) {
	packet, hash, err := encodePacket(t.priv, ptype, req)
	if err != nil {
		return hash, err
	}
	return hash, t.write(toaddr, req.name(), packet)
}

func (t *udp) write(toaddr *net.UDPAddr, what string, packet []byte) error {
	_, err := t.conn.WriteToUDP(packet, toaddr)
	log4j.Trace(">> "+what, "addr", toaddr, "err", err)
	return err
}

func encodePacket(priv *ecdsa.PrivateKey, ptype byte, req interface{}) (packet, hash []byte, err error) {
	b := new(bytes.Buffer)
	b.Write(headSpace)
	b.WriteByte(ptype)
	if err := rlp.Encode(b, req); err != nil {
		log4j.Error("Can't encode discv4 packet", "err", err)
		return nil, nil, err
	}
	packet = b.Bytes()
	sig, err := crypto.Sign(crypto.Keccak256(packet[headSize:]), priv)
	if err != nil {
		log4j.Error("Can't sign discv4 packet", "err", err)
		return nil, nil, err
	}
	copy(packet[macSize:], sig)
	hash = crypto.Keccak256(packet[macSize:])
	copy(packet, hash)
	return packet, hash, nil
}

func (t *udp) readLoop(unhandled chan<- ReadPacket) {
	defer t.wg.Done()
	if unhandled != nil {
		defer close(unhandled)
	}

	buf := make([]byte, 1280)
	for {
		nbytes, from, err := t.conn.ReadFromUDP(buf)
		if netutil.IsTemporaryError(err) {
			log4j.Debug("Temporary UDP read error", "err", err)
			continue
		} else if err != nil {
			log4j.Debug("UDP read error", "err", err)
			return
		}
		if t.handlePacket(from, buf[:nbytes]) != nil && unhandled != nil {
			select {
			case unhandled <- ReadPacket{buf[:nbytes], from}:
			default:
			}
		}
	}
}

func (t *udp) handlePacket(from *net.UDPAddr, buf []byte) error {
	packet, fromID, hash, err := decodePacket(buf)
	if err != nil {
		log4j.Debug("Bad discv4 packet", "addr", from, "err", err)
		return err
	}
	err = packet.handle(t, from, fromID, hash)
	log4j.Trace("<< "+packet.name(), "addr", from, "err", err)
	return err
}

func decodePacket(buf []byte) (packet, encPubkey, []byte, error) {
	if len(buf) < headSize+1 {
		return nil, encPubkey{}, nil, errPacketTooSmall
	}
	hash, sig, sigdata := buf[:macSize], buf[macSize:headSize], buf[headSize:]
	shouldhash := crypto.Keccak256(buf[macSize:])
	if !bytes.Equal(hash, shouldhash) {
		return nil, encPubkey{}, nil, errBadHash
	}
	fromKey, err := recoverNodeKey(crypto.Keccak256(buf[headSize:]), sig)
	if err != nil {
		return nil, fromKey, hash, err
	}

	var req packet
	switch ptype := sigdata[0]; ptype {
	case pingPacket:
		req = new(ping)
	case pongPacket:
		req = new(pong)
	case findnodePacket:
		req = new(findnode)
	case neighborsPacket:
		req = new(neighbors)
	default:
		return nil, fromKey, hash, fmt.Errorf("unknown type: %d", ptype)
	}
	s := rlp.NewStream(bytes.NewReader(sigdata[1:]), 0)
	err = s.Decode(req)
	return req, fromKey, hash, err
}

func (req *ping) handle(t *udp, from *net.UDPAddr, fromKey encPubkey, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	key, err := decodePubkey(fromKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %v", err)
	}
	t.send(from, pongPacket, &pong{
		To:         makeEndpoint(from, req.From.TCP),
		ReplyTok:   mac,
		Expiration: uint64(time.Now().Add(expiration).Unix()),
	})
	n := wrapNode(cnode.NewV4(key, from.IP, int(req.From.TCP), from.Port))
	t.handleReply(n.ID(), pingPacket, req)
	if time.Since(t.db.LastPongReceived(n.ID())) > bondExpiration {
		t.sendPing(n.ID(), from, func() { t.tab.addThroughPing(n) })
	} else {
		t.tab.addThroughPing(n)
	}
	t.localNode.UDPEndpointStatement(from, &net.UDPAddr{IP: req.To.IP, Port: int(req.To.UDP)})
	t.db.UpdateLastPingReceived(n.ID(), time.Now())
	return nil
}

func (req *ping) name() string { return "PING/v4" }

func (req *pong) handle(t *udp, from *net.UDPAddr, fromKey encPubkey, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	fromID := fromKey.id()
	if !t.handleReply(fromID, pongPacket, req) {
		return errUnsolicitedReply
	}
	t.localNode.UDPEndpointStatement(from, &net.UDPAddr{IP: req.To.IP, Port: int(req.To.UDP)})
	t.db.UpdateLastPongReceived(fromID, time.Now())
	return nil
}

func (req *pong) name() string { return "PONG/v4" }

func (req *findnode) handle(t *udp, from *net.UDPAddr, fromKey encPubkey, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	fromID := fromKey.id()
	if time.Since(t.db.LastPongReceived(fromID)) > bondExpiration {
		return errUnknownNode
	}
	target := cnode.ID(crypto.Keccak256Hash(req.Target[:]))
	t.tab.mutex.Lock()
	closest := t.tab.closest(target, bucketSize).entries
	t.tab.mutex.Unlock()

	p := neighbors{Expiration: uint64(time.Now().Add(expiration).Unix())}
	var sent bool
	for _, n := range closest {
		if netutil.CheckRelayIP(from.IP, n.IP()) == nil {
			p.Nodes = append(p.Nodes, nodeToRPC(n))
		}
		if len(p.Nodes) == maxNeighbors {
			t.send(from, neighborsPacket, &p)
			p.Nodes = p.Nodes[:0]
			sent = true
		}
	}
	if len(p.Nodes) > 0 || !sent {
		t.send(from, neighborsPacket, &p)
	}
	return nil
}

func (req *findnode) name() string { return "FINDNODE/v4" }

func (req *neighbors) handle(t *udp, from *net.UDPAddr, fromKey encPubkey, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	if !t.handleReply(fromKey.id(), neighborsPacket, req) {
		return errUnsolicitedReply
	}
	return nil
}

func (req *neighbors) name() string { return "NEIGHBORS/v4" }

func expired(ts uint64) bool {
	return time.Unix(int64(ts), 0).Before(time.Now())
}
