package p2p

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/mclock"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/lib/p2p/discv5"
	"github.com/5uwifi/canchain/lib/p2p/enr"
	"github.com/5uwifi/canchain/lib/p2p/nat"
	"github.com/5uwifi/canchain/lib/p2p/netutil"
	"github.com/5uwifi/canchain/lib/rlp"
)

const (
	defaultDialTimeout = 15 * time.Second

	maxActiveDialTasks     = 16
	defaultMaxPendingPeers = 50
	defaultDialRatio       = 3

	frameReadTimeout = 30 * time.Second

	frameWriteTimeout = 20 * time.Second
)

var errServerStopped = errors.New("server stopped")

type Config struct {
	PrivateKey *ecdsa.PrivateKey `toml:"-"`

	MaxPeers int

	MaxPendingPeers int `toml:",omitempty"`

	DialRatio int `toml:",omitempty"`

	NoDiscovery bool

	DiscoveryV5 bool `toml:",omitempty"`

	Name string `toml:"-"`

	BootstrapNodes []*cnode.Node

	BootstrapNodesV5 []*discv5.Node `toml:",omitempty"`

	StaticNodes []*cnode.Node

	TrustedNodes []*cnode.Node

	NetRestrict *netutil.Netlist `toml:",omitempty"`

	NodeDatabase string `toml:",omitempty"`

	Protocols []Protocol `toml:"-"`

	ListenAddr string

	NAT nat.Interface `toml:",omitempty"`

	Dialer NodeDialer `toml:"-"`

	NoDial bool `toml:",omitempty"`

	EnableMsgEvents bool

	Logger log4j.Logger `toml:",omitempty"`
}

type Server struct {
	Config

	newTransport func(net.Conn) transport
	newPeerHook  func(*Peer)

	lock    sync.Mutex
	running bool

	nodedb       *cnode.DB
	localnode    *cnode.LocalNode
	ntab         discoverTable
	listener     net.Listener
	ourHandshake *protoHandshake
	lastLookup   time.Time
	DiscV5       *discv5.Network

	peerOp     chan peerOpFunc
	peerOpDone chan struct{}

	quit          chan struct{}
	addstatic     chan *cnode.Node
	removestatic  chan *cnode.Node
	addtrusted    chan *cnode.Node
	removetrusted chan *cnode.Node
	posthandshake chan *conn
	addpeer       chan *conn
	delpeer       chan peerDrop
	loopWG        sync.WaitGroup
	peerFeed      event.Feed
	log           log4j.Logger
}

type peerOpFunc func(map[cnode.ID]*Peer)

type peerDrop struct {
	*Peer
	err       error
	requested bool
}

type connFlag int32

const (
	dynDialedConn connFlag = 1 << iota
	staticDialedConn
	inboundConn
	trustedConn
)

type conn struct {
	fd net.Conn
	transport
	node  *cnode.Node
	flags connFlag
	cont  chan error
	caps  []Cap
	name  string
}

type transport interface {
	doEncHandshake(prv *ecdsa.PrivateKey, dialDest *ecdsa.PublicKey) (*ecdsa.PublicKey, error)
	doProtoHandshake(our *protoHandshake) (*protoHandshake, error)
	MsgReadWriter
	close(err error)
}

func (c *conn) String() string {
	s := c.flags.String()
	if (c.node.ID() != cnode.ID{}) {
		s += " " + c.node.ID().String()
	}
	s += " " + c.fd.RemoteAddr().String()
	return s
}

func (f connFlag) String() string {
	s := ""
	if f&trustedConn != 0 {
		s += "-trusted"
	}
	if f&dynDialedConn != 0 {
		s += "-dyndial"
	}
	if f&staticDialedConn != 0 {
		s += "-staticdial"
	}
	if f&inboundConn != 0 {
		s += "-inbound"
	}
	if s != "" {
		s = s[1:]
	}
	return s
}

func (c *conn) is(f connFlag) bool {
	flags := connFlag(atomic.LoadInt32((*int32)(&c.flags)))
	return flags&f != 0
}

func (c *conn) set(f connFlag, val bool) {
	for {
		oldFlags := connFlag(atomic.LoadInt32((*int32)(&c.flags)))
		flags := oldFlags
		if val {
			flags |= f
		} else {
			flags &= ^f
		}
		if atomic.CompareAndSwapInt32((*int32)(&c.flags), int32(oldFlags), int32(flags)) {
			return
		}
	}
}

func (srv *Server) Peers() []*Peer {
	var ps []*Peer
	select {
	case srv.peerOp <- func(peers map[cnode.ID]*Peer) {
		for _, p := range peers {
			ps = append(ps, p)
		}
	}:
		<-srv.peerOpDone
	case <-srv.quit:
	}
	return ps
}

func (srv *Server) PeerCount() int {
	var count int
	select {
	case srv.peerOp <- func(ps map[cnode.ID]*Peer) { count = len(ps) }:
		<-srv.peerOpDone
	case <-srv.quit:
	}
	return count
}

func (srv *Server) AddPeer(node *cnode.Node) {
	select {
	case srv.addstatic <- node:
	case <-srv.quit:
	}
}

func (srv *Server) RemovePeer(node *cnode.Node) {
	select {
	case srv.removestatic <- node:
	case <-srv.quit:
	}
}

func (srv *Server) AddTrustedPeer(node *cnode.Node) {
	select {
	case srv.addtrusted <- node:
	case <-srv.quit:
	}
}

func (srv *Server) RemoveTrustedPeer(node *cnode.Node) {
	select {
	case srv.removetrusted <- node:
	case <-srv.quit:
	}
}

func (srv *Server) SubscribeEvents(ch chan *PeerEvent) event.Subscription {
	return srv.peerFeed.Subscribe(ch)
}

func (srv *Server) Self() *cnode.Node {
	srv.lock.Lock()
	ln := srv.localnode
	srv.lock.Unlock()

	if ln == nil {
		return cnode.NewV4(&srv.PrivateKey.PublicKey, net.ParseIP("0.0.0.0"), 0, 0)
	}
	return ln.Node()
}

func (srv *Server) Stop() {
	srv.lock.Lock()
	if !srv.running {
		srv.lock.Unlock()
		return
	}
	srv.running = false
	if srv.listener != nil {
		srv.listener.Close()
	}
	close(srv.quit)
	srv.lock.Unlock()
	srv.loopWG.Wait()
}

type sharedUDPConn struct {
	*net.UDPConn
	unhandled chan discover.ReadPacket
}

func (s *sharedUDPConn) ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error) {
	packet, ok := <-s.unhandled
	if !ok {
		return 0, nil, fmt.Errorf("Connection was closed")
	}
	l := len(packet.Data)
	if l > len(b) {
		l = len(b)
	}
	copy(b[:l], packet.Data[:l])
	return l, packet.Addr, nil
}

func (s *sharedUDPConn) Close() error {
	return nil
}

func (srv *Server) Start() (err error) {
	srv.lock.Lock()
	defer srv.lock.Unlock()
	if srv.running {
		return errors.New("server already running")
	}
	srv.running = true
	srv.log = srv.Config.Logger
	if srv.log == nil {
		srv.log = log4j.New()
	}
	if srv.NoDial && srv.ListenAddr == "" {
		srv.log.Warn("P2P server will be useless, neither dialing nor listening")
	}

	if srv.PrivateKey == nil {
		return fmt.Errorf("Server.PrivateKey must be set to a non-nil key")
	}
	if srv.newTransport == nil {
		srv.newTransport = newRLPX
	}
	if srv.Dialer == nil {
		srv.Dialer = TCPDialer{&net.Dialer{Timeout: defaultDialTimeout}}
	}
	srv.quit = make(chan struct{})
	srv.addpeer = make(chan *conn)
	srv.delpeer = make(chan peerDrop)
	srv.posthandshake = make(chan *conn)
	srv.addstatic = make(chan *cnode.Node)
	srv.removestatic = make(chan *cnode.Node)
	srv.addtrusted = make(chan *cnode.Node)
	srv.removetrusted = make(chan *cnode.Node)
	srv.peerOp = make(chan peerOpFunc)
	srv.peerOpDone = make(chan struct{})

	if err := srv.setupLocalNode(); err != nil {
		return err
	}
	if srv.ListenAddr != "" {
		if err := srv.setupListening(); err != nil {
			return err
		}
	}
	if err := srv.setupDiscovery(); err != nil {
		return err
	}

	dynPeers := srv.maxDialedConns()
	dialer := newDialState(srv.localnode.ID(), srv.StaticNodes, srv.BootstrapNodes, srv.ntab, dynPeers, srv.NetRestrict)
	srv.loopWG.Add(1)
	go srv.run(dialer)
	return nil
}

func (srv *Server) setupLocalNode() error {
	pubkey := crypto.FromECDSAPub(&srv.PrivateKey.PublicKey)
	srv.ourHandshake = &protoHandshake{Version: baseProtocolVersion, Name: srv.Name, ID: pubkey[1:]}
	for _, p := range srv.Protocols {
		srv.ourHandshake.Caps = append(srv.ourHandshake.Caps, p.cap())
	}
	sort.Sort(capsByNameAndVersion(srv.ourHandshake.Caps))

	db, err := cnode.OpenDB(srv.Config.NodeDatabase)
	if err != nil {
		return err
	}
	srv.nodedb = db
	srv.localnode = cnode.NewLocalNode(db, srv.PrivateKey)
	srv.localnode.SetFallbackIP(net.IP{127, 0, 0, 1})
	srv.localnode.Set(capsByNameAndVersion(srv.ourHandshake.Caps))
	for _, p := range srv.Protocols {
		for _, e := range p.Attributes {
			srv.localnode.Set(e)
		}
	}
	switch srv.NAT.(type) {
	case nil:
	case nat.ExtIP:
		ip, _ := srv.NAT.ExternalIP()
		srv.localnode.SetStaticIP(ip)
	default:
		srv.loopWG.Add(1)
		go func() {
			defer srv.loopWG.Done()
			if ip, err := srv.NAT.ExternalIP(); err == nil {
				srv.localnode.SetStaticIP(ip)
			}
		}()
	}
	return nil
}

func (srv *Server) setupDiscovery() error {
	if srv.NoDiscovery && !srv.DiscoveryV5 {
		return nil
	}

	addr, err := net.ResolveUDPAddr("udp", srv.ListenAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	realaddr := conn.LocalAddr().(*net.UDPAddr)
	srv.log.Debug("UDP listener up", "addr", realaddr)
	if srv.NAT != nil {
		if !realaddr.IP.IsLoopback() {
			go nat.Map(srv.NAT, srv.quit, "udp", realaddr.Port, realaddr.Port, "canchain discovery")
		}
	}
	srv.localnode.SetFallbackUDP(realaddr.Port)

	var unhandled chan discover.ReadPacket
	var sconn *sharedUDPConn
	if !srv.NoDiscovery {
		if srv.DiscoveryV5 {
			unhandled = make(chan discover.ReadPacket, 100)
			sconn = &sharedUDPConn{conn, unhandled}
		}
		cfg := discover.Config{
			PrivateKey:  srv.PrivateKey,
			NetRestrict: srv.NetRestrict,
			Bootnodes:   srv.BootstrapNodes,
			Unhandled:   unhandled,
		}
		ntab, err := discover.ListenUDP(conn, srv.localnode, cfg)
		if err != nil {
			return err
		}
		srv.ntab = ntab
	}
	if srv.DiscoveryV5 {
		var ntab *discv5.Network
		var err error
		if sconn != nil {
			ntab, err = discv5.ListenUDP(srv.PrivateKey, sconn, "", srv.NetRestrict)
		} else {
			ntab, err = discv5.ListenUDP(srv.PrivateKey, conn, "", srv.NetRestrict)
		}
		if err != nil {
			return err
		}
		if err := ntab.SetFallbackNodes(srv.BootstrapNodesV5); err != nil {
			return err
		}
		srv.DiscV5 = ntab
	}
	return nil
}

func (srv *Server) setupListening() error {
	listener, err := net.Listen("tcp", srv.ListenAddr)
	if err != nil {
		return err
	}
	laddr := listener.Addr().(*net.TCPAddr)
	srv.ListenAddr = laddr.String()
	srv.listener = listener
	srv.localnode.Set(enr.TCP(laddr.Port))

	srv.loopWG.Add(1)
	go srv.listenLoop()

	if !laddr.IP.IsLoopback() && srv.NAT != nil {
		srv.loopWG.Add(1)
		go func() {
			nat.Map(srv.NAT, srv.quit, "tcp", laddr.Port, laddr.Port, "ethereum p2p")
			srv.loopWG.Done()
		}()
	}
	return nil
}

type dialer interface {
	newTasks(running int, peers map[cnode.ID]*Peer, now time.Time) []task
	taskDone(task, time.Time)
	addStatic(*cnode.Node)
	removeStatic(*cnode.Node)
}

func (srv *Server) run(dialstate dialer) {
	srv.log.Info("Started P2P networking", "self", srv.localnode.Node())
	defer srv.loopWG.Done()
	defer srv.nodedb.Close()

	var (
		peers        = make(map[cnode.ID]*Peer)
		inboundCount = 0
		trusted      = make(map[cnode.ID]bool, len(srv.TrustedNodes))
		taskdone     = make(chan task, maxActiveDialTasks)
		runningTasks []task
		queuedTasks  []task
	)
	for _, n := range srv.TrustedNodes {
		trusted[n.ID()] = true
	}

	delTask := func(t task) {
		for i := range runningTasks {
			if runningTasks[i] == t {
				runningTasks = append(runningTasks[:i], runningTasks[i+1:]...)
				break
			}
		}
	}
	startTasks := func(ts []task) (rest []task) {
		i := 0
		for ; len(runningTasks) < maxActiveDialTasks && i < len(ts); i++ {
			t := ts[i]
			srv.log.Trace("New dial task", "task", t)
			go func() { t.Do(srv); taskdone <- t }()
			runningTasks = append(runningTasks, t)
		}
		return ts[i:]
	}
	scheduleTasks := func() {
		queuedTasks = append(queuedTasks[:0], startTasks(queuedTasks)...)
		if len(runningTasks) < maxActiveDialTasks {
			nt := dialstate.newTasks(len(runningTasks)+len(queuedTasks), peers, time.Now())
			queuedTasks = append(queuedTasks, startTasks(nt)...)
		}
	}

running:
	for {
		scheduleTasks()

		select {
		case <-srv.quit:
			break running
		case n := <-srv.addstatic:
			srv.log.Trace("Adding static node", "node", n)
			dialstate.addStatic(n)
		case n := <-srv.removestatic:
			srv.log.Trace("Removing static node", "node", n)
			dialstate.removeStatic(n)
			if p, ok := peers[n.ID()]; ok {
				p.Disconnect(DiscRequested)
			}
		case n := <-srv.addtrusted:
			srv.log.Trace("Adding trusted node", "node", n)
			trusted[n.ID()] = true
			if p, ok := peers[n.ID()]; ok {
				p.rw.set(trustedConn, true)
			}
		case n := <-srv.removetrusted:
			srv.log.Trace("Removing trusted node", "node", n)
			if _, ok := trusted[n.ID()]; ok {
				delete(trusted, n.ID())
			}
			if p, ok := peers[n.ID()]; ok {
				p.rw.set(trustedConn, false)
			}
		case op := <-srv.peerOp:
			op(peers)
			srv.peerOpDone <- struct{}{}
		case t := <-taskdone:
			srv.log.Trace("Dial task done", "task", t)
			dialstate.taskDone(t, time.Now())
			delTask(t)
		case c := <-srv.posthandshake:
			if trusted[c.node.ID()] {
				c.flags |= trustedConn
			}
			select {
			case c.cont <- srv.encHandshakeChecks(peers, inboundCount, c):
			case <-srv.quit:
				break running
			}
		case c := <-srv.addpeer:
			err := srv.protoHandshakeChecks(peers, inboundCount, c)
			if err == nil {
				p := newPeer(c, srv.Protocols)
				if srv.EnableMsgEvents {
					p.events = &srv.peerFeed
				}
				name := truncateName(c.name)
				srv.log.Debug("Adding p2p peer", "name", name, "addr", c.fd.RemoteAddr(), "peers", len(peers)+1)
				go srv.runPeer(p)
				peers[c.node.ID()] = p
				if p.Inbound() {
					inboundCount++
				}
			}
			select {
			case c.cont <- err:
			case <-srv.quit:
				break running
			}
		case pd := <-srv.delpeer:
			d := common.PrettyDuration(mclock.Now() - pd.created)
			pd.log.Debug("Removing p2p peer", "duration", d, "peers", len(peers)-1, "req", pd.requested, "err", pd.err)
			delete(peers, pd.ID())
			if pd.Inbound() {
				inboundCount--
			}
		}
	}

	srv.log.Trace("P2P networking is spinning down")

	if srv.ntab != nil {
		srv.ntab.Close()
	}
	if srv.DiscV5 != nil {
		srv.DiscV5.Close()
	}
	for _, p := range peers {
		p.Disconnect(DiscQuitting)
	}
	for len(peers) > 0 {
		p := <-srv.delpeer
		p.log.Trace("<-delpeer (spindown)", "remainingTasks", len(runningTasks))
		delete(peers, p.ID())
	}
}

func (srv *Server) protoHandshakeChecks(peers map[cnode.ID]*Peer, inboundCount int, c *conn) error {
	if len(srv.Protocols) > 0 && countMatchingProtocols(srv.Protocols, c.caps) == 0 {
		return DiscUselessPeer
	}
	return srv.encHandshakeChecks(peers, inboundCount, c)
}

func (srv *Server) encHandshakeChecks(peers map[cnode.ID]*Peer, inboundCount int, c *conn) error {
	switch {
	case !c.is(trustedConn|staticDialedConn) && len(peers) >= srv.MaxPeers:
		return DiscTooManyPeers
	case !c.is(trustedConn) && c.is(inboundConn) && inboundCount >= srv.maxInboundConns():
		return DiscTooManyPeers
	case peers[c.node.ID()] != nil:
		return DiscAlreadyConnected
	case c.node.ID() == srv.localnode.ID():
		return DiscSelf
	default:
		return nil
	}
}

func (srv *Server) maxInboundConns() int {
	return srv.MaxPeers - srv.maxDialedConns()
}
func (srv *Server) maxDialedConns() int {
	if srv.NoDiscovery || srv.NoDial {
		return 0
	}
	r := srv.DialRatio
	if r == 0 {
		r = defaultDialRatio
	}
	return srv.MaxPeers / r
}

func (srv *Server) listenLoop() {
	defer srv.loopWG.Done()
	srv.log.Debug("TCP listener up", "addr", srv.listener.Addr())

	tokens := defaultMaxPendingPeers
	if srv.MaxPendingPeers > 0 {
		tokens = srv.MaxPendingPeers
	}
	slots := make(chan struct{}, tokens)
	for i := 0; i < tokens; i++ {
		slots <- struct{}{}
	}

	for {
		<-slots

		var (
			fd  net.Conn
			err error
		)
		for {
			fd, err = srv.listener.Accept()
			if netutil.IsTemporaryError(err) {
				srv.log.Debug("Temporary read error", "err", err)
				continue
			} else if err != nil {
				srv.log.Debug("Read error", "err", err)
				return
			}
			break
		}

		if srv.NetRestrict != nil {
			if tcp, ok := fd.RemoteAddr().(*net.TCPAddr); ok && !srv.NetRestrict.Contains(tcp.IP) {
				srv.log.Debug("Rejected conn (not whitelisted in NetRestrict)", "addr", fd.RemoteAddr())
				fd.Close()
				slots <- struct{}{}
				continue
			}
		}

		fd = newMeteredConn(fd, true)
		srv.log.Trace("Accepted connection", "addr", fd.RemoteAddr())
		go func() {
			srv.SetupConn(fd, inboundConn, nil)
			slots <- struct{}{}
		}()
	}
}

func (srv *Server) SetupConn(fd net.Conn, flags connFlag, dialDest *cnode.Node) error {
	c := &conn{fd: fd, transport: srv.newTransport(fd), flags: flags, cont: make(chan error)}
	err := srv.setupConn(c, flags, dialDest)
	if err != nil {
		c.close(err)
		srv.log.Trace("Setting up connection failed", "addr", fd.RemoteAddr(), "err", err)
	}
	return err
}

func (srv *Server) setupConn(c *conn, flags connFlag, dialDest *cnode.Node) error {
	srv.lock.Lock()
	running := srv.running
	srv.lock.Unlock()
	if !running {
		return errServerStopped
	}
	var dialPubkey *ecdsa.PublicKey
	if dialDest != nil {
		dialPubkey = new(ecdsa.PublicKey)
		if err := dialDest.Load((*cnode.Secp256k1)(dialPubkey)); err != nil {
			return fmt.Errorf("dial destination doesn't have a secp256k1 public key")
		}
	}
	remotePubkey, err := c.doEncHandshake(srv.PrivateKey, dialPubkey)
	if err != nil {
		srv.log.Trace("Failed RLPx handshake", "addr", c.fd.RemoteAddr(), "conn", c.flags, "err", err)
		return err
	}
	if dialDest != nil {
		if dialPubkey.X.Cmp(remotePubkey.X) != 0 || dialPubkey.Y.Cmp(remotePubkey.Y) != 0 {
			return DiscUnexpectedIdentity
		}
		c.node = dialDest
	} else {
		c.node = nodeFromConn(remotePubkey, c.fd)
	}
	clog := srv.log.New("id", c.node.ID(), "addr", c.fd.RemoteAddr(), "conn", c.flags)
	err = srv.checkpoint(c, srv.posthandshake)
	if err != nil {
		clog.Trace("Rejected peer before protocol handshake", "err", err)
		return err
	}
	phs, err := c.doProtoHandshake(srv.ourHandshake)
	if err != nil {
		clog.Trace("Failed proto handshake", "err", err)
		return err
	}
	if id := c.node.ID(); !bytes.Equal(crypto.Keccak256(phs.ID), id[:]) {
		clog.Trace("Wrong devp2p handshake identity", "phsid", fmt.Sprintf("%x", phs.ID))
		return DiscUnexpectedIdentity
	}
	c.caps, c.name = phs.Caps, phs.Name
	err = srv.checkpoint(c, srv.addpeer)
	if err != nil {
		clog.Trace("Rejected peer", "err", err)
		return err
	}
	clog.Trace("connection set up", "inbound", dialDest == nil)
	return nil
}

func nodeFromConn(pubkey *ecdsa.PublicKey, conn net.Conn) *cnode.Node {
	var ip net.IP
	var port int
	if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		ip = tcp.IP
		port = tcp.Port
	}
	return cnode.NewV4(pubkey, ip, port, port)
}

func truncateName(s string) string {
	if len(s) > 20 {
		return s[:20] + "..."
	}
	return s
}

func (srv *Server) checkpoint(c *conn, stage chan<- *conn) error {
	select {
	case stage <- c:
	case <-srv.quit:
		return errServerStopped
	}
	select {
	case err := <-c.cont:
		return err
	case <-srv.quit:
		return errServerStopped
	}
}

func (srv *Server) runPeer(p *Peer) {
	if srv.newPeerHook != nil {
		srv.newPeerHook(p)
	}

	srv.peerFeed.Send(&PeerEvent{
		Type: PeerEventTypeAdd,
		Peer: p.ID(),
	})

	remoteRequested, err := p.run()

	srv.peerFeed.Send(&PeerEvent{
		Type:  PeerEventTypeDrop,
		Peer:  p.ID(),
		Error: err.Error(),
	})

	srv.delpeer <- peerDrop{p, err, remoteRequested}
}

type NodeInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	CCnode string `json:"ccnode"`
	ENR    string `json:"enr"`
	IP     string `json:"ip"`
	Ports  struct {
		Discovery int `json:"discovery"`
		Listener  int `json:"listener"`
	} `json:"ports"`
	ListenAddr string                 `json:"listenAddr"`
	Protocols  map[string]interface{} `json:"protocols"`
}

func (srv *Server) NodeInfo() *NodeInfo {
	node := srv.Self()
	info := &NodeInfo{
		Name:       srv.Name,
		CCnode:     node.String(),
		ID:         node.ID().String(),
		IP:         node.IP().String(),
		ListenAddr: srv.ListenAddr,
		Protocols:  make(map[string]interface{}),
	}
	info.Ports.Discovery = node.UDP()
	info.Ports.Listener = node.TCP()
	if enc, err := rlp.EncodeToBytes(node.Record()); err == nil {
		info.ENR = "0x" + hex.EncodeToString(enc)
	}

	for _, proto := range srv.Protocols {
		if _, ok := info.Protocols[proto.Name]; !ok {
			nodeInfo := interface{}("unknown")
			if query := proto.NodeInfo; query != nil {
				nodeInfo = proto.NodeInfo()
			}
			info.Protocols[proto.Name] = nodeInfo
		}
	}
	return info
}

func (srv *Server) PeersInfo() []*PeerInfo {
	infos := make([]*PeerInfo, 0, srv.PeerCount())
	for _, peer := range srv.Peers() {
		if peer != nil {
			infos = append(infos, peer.Info())
		}
	}
	for i := 0; i < len(infos); i++ {
		for j := i + 1; j < len(infos); j++ {
			if infos[i].ID > infos[j].ID {
				infos[i], infos[j] = infos[j], infos[i]
			}
		}
	}
	return infos
}
