package p2p

import (
	"container/heap"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/netutil"
)

const (
	dialHistoryExpiration = 30 * time.Second

	lookupInterval = 4 * time.Second

	fallbackInterval = 20 * time.Second

	initialResolveDelay = 60 * time.Second
	maxResolveDelay     = time.Hour
)

type NodeDialer interface {
	Dial(*cnode.Node) (net.Conn, error)
}

type TCPDialer struct {
	*net.Dialer
}

func (t TCPDialer) Dial(dest *cnode.Node) (net.Conn, error) {
	addr := &net.TCPAddr{IP: dest.IP(), Port: dest.TCP()}
	return t.Dialer.Dial("tcp", addr.String())
}

type dialstate struct {
	maxDynDials int
	ntab        discoverTable
	netrestrict *netutil.Netlist
	self        cnode.ID

	lookupRunning bool
	dialing       map[cnode.ID]connFlag
	lookupBuf     []*cnode.Node
	randomNodes   []*cnode.Node
	static        map[cnode.ID]*dialTask
	hist          *dialHistory

	start     time.Time
	bootnodes []*cnode.Node
}

type discoverTable interface {
	Close()
	Resolve(*cnode.Node) *cnode.Node
	LookupRandom() []*cnode.Node
	ReadRandomNodes([]*cnode.Node) int
}

type dialHistory []pastDial

type pastDial struct {
	id  cnode.ID
	exp time.Time
}

type task interface {
	Do(*Server)
}

type dialTask struct {
	flags        connFlag
	dest         *cnode.Node
	lastResolved time.Time
	resolveDelay time.Duration
}

type discoverTask struct {
	results []*cnode.Node
}

type waitExpireTask struct {
	time.Duration
}

func newDialState(self cnode.ID, static []*cnode.Node, bootnodes []*cnode.Node, ntab discoverTable, maxdyn int, netrestrict *netutil.Netlist) *dialstate {
	s := &dialstate{
		maxDynDials: maxdyn,
		ntab:        ntab,
		self:        self,
		netrestrict: netrestrict,
		static:      make(map[cnode.ID]*dialTask),
		dialing:     make(map[cnode.ID]connFlag),
		bootnodes:   make([]*cnode.Node, len(bootnodes)),
		randomNodes: make([]*cnode.Node, maxdyn/2),
		hist:        new(dialHistory),
	}
	copy(s.bootnodes, bootnodes)
	for _, n := range static {
		s.addStatic(n)
	}
	return s
}

func (s *dialstate) addStatic(n *cnode.Node) {
	s.static[n.ID()] = &dialTask{flags: staticDialedConn, dest: n}
}

func (s *dialstate) removeStatic(n *cnode.Node) {
	delete(s.static, n.ID())
	s.hist.remove(n.ID())
}

func (s *dialstate) newTasks(nRunning int, peers map[cnode.ID]*Peer, now time.Time) []task {
	if s.start.IsZero() {
		s.start = now
	}

	var newtasks []task
	addDial := func(flag connFlag, n *cnode.Node) bool {
		if err := s.checkDial(n, peers); err != nil {
			log4j.Trace("Skipping dial candidate", "id", n.ID(), "addr", &net.TCPAddr{IP: n.IP(), Port: n.TCP()}, "err", err)
			return false
		}
		s.dialing[n.ID()] = flag
		newtasks = append(newtasks, &dialTask{flags: flag, dest: n})
		return true
	}

	needDynDials := s.maxDynDials
	for _, p := range peers {
		if p.rw.is(dynDialedConn) {
			needDynDials--
		}
	}
	for _, flag := range s.dialing {
		if flag&dynDialedConn != 0 {
			needDynDials--
		}
	}

	s.hist.expire(now)

	for id, t := range s.static {
		err := s.checkDial(t.dest, peers)
		switch err {
		case errNotWhitelisted, errSelf:
			log4j.Warn("Removing static dial candidate", "id", t.dest.ID, "addr", &net.TCPAddr{IP: t.dest.IP(), Port: t.dest.TCP()}, "err", err)
			delete(s.static, t.dest.ID())
		case nil:
			s.dialing[id] = t.flags
			newtasks = append(newtasks, t)
		}
	}
	if len(peers) == 0 && len(s.bootnodes) > 0 && needDynDials > 0 && now.Sub(s.start) > fallbackInterval {
		bootnode := s.bootnodes[0]
		s.bootnodes = append(s.bootnodes[:0], s.bootnodes[1:]...)
		s.bootnodes = append(s.bootnodes, bootnode)

		if addDial(dynDialedConn, bootnode) {
			needDynDials--
		}
	}
	randomCandidates := needDynDials / 2
	if randomCandidates > 0 {
		n := s.ntab.ReadRandomNodes(s.randomNodes)
		for i := 0; i < randomCandidates && i < n; i++ {
			if addDial(dynDialedConn, s.randomNodes[i]) {
				needDynDials--
			}
		}
	}
	i := 0
	for ; i < len(s.lookupBuf) && needDynDials > 0; i++ {
		if addDial(dynDialedConn, s.lookupBuf[i]) {
			needDynDials--
		}
	}
	s.lookupBuf = s.lookupBuf[:copy(s.lookupBuf, s.lookupBuf[i:])]
	if len(s.lookupBuf) < needDynDials && !s.lookupRunning {
		s.lookupRunning = true
		newtasks = append(newtasks, &discoverTask{})
	}

	if nRunning == 0 && len(newtasks) == 0 && s.hist.Len() > 0 {
		t := &waitExpireTask{s.hist.min().exp.Sub(now)}
		newtasks = append(newtasks, t)
	}
	return newtasks
}

var (
	errSelf             = errors.New("is self")
	errAlreadyDialing   = errors.New("already dialing")
	errAlreadyConnected = errors.New("already connected")
	errRecentlyDialed   = errors.New("recently dialed")
	errNotWhitelisted   = errors.New("not contained in netrestrict whitelist")
)

func (s *dialstate) checkDial(n *cnode.Node, peers map[cnode.ID]*Peer) error {
	_, dialing := s.dialing[n.ID()]
	switch {
	case dialing:
		return errAlreadyDialing
	case peers[n.ID()] != nil:
		return errAlreadyConnected
	case n.ID() == s.self:
		return errSelf
	case s.netrestrict != nil && !s.netrestrict.Contains(n.IP()):
		return errNotWhitelisted
	case s.hist.contains(n.ID()):
		return errRecentlyDialed
	}
	return nil
}

func (s *dialstate) taskDone(t task, now time.Time) {
	switch t := t.(type) {
	case *dialTask:
		s.hist.add(t.dest.ID(), now.Add(dialHistoryExpiration))
		delete(s.dialing, t.dest.ID())
	case *discoverTask:
		s.lookupRunning = false
		s.lookupBuf = append(s.lookupBuf, t.results...)
	}
}

func (t *dialTask) Do(srv *Server) {
	if t.dest.Incomplete() {
		if !t.resolve(srv) {
			return
		}
	}
	err := t.dial(srv, t.dest)
	if err != nil {
		log4j.Trace("Dial error", "task", t, "err", err)
		if _, ok := err.(*dialError); ok && t.flags&staticDialedConn != 0 {
			if t.resolve(srv) {
				t.dial(srv, t.dest)
			}
		}
	}
}

func (t *dialTask) resolve(srv *Server) bool {
	if srv.ntab == nil {
		log4j.Debug("Can't resolve node", "id", t.dest.ID, "err", "discovery is disabled")
		return false
	}
	if t.resolveDelay == 0 {
		t.resolveDelay = initialResolveDelay
	}
	if time.Since(t.lastResolved) < t.resolveDelay {
		return false
	}
	resolved := srv.ntab.Resolve(t.dest)
	t.lastResolved = time.Now()
	if resolved == nil {
		t.resolveDelay *= 2
		if t.resolveDelay > maxResolveDelay {
			t.resolveDelay = maxResolveDelay
		}
		log4j.Debug("Resolving node failed", "id", t.dest.ID, "newdelay", t.resolveDelay)
		return false
	}
	t.resolveDelay = initialResolveDelay
	t.dest = resolved
	log4j.Debug("Resolved node", "id", t.dest.ID, "addr", &net.TCPAddr{IP: t.dest.IP(), Port: t.dest.TCP()})
	return true
}

type dialError struct {
	error
}

func (t *dialTask) dial(srv *Server, dest *cnode.Node) error {
	fd, err := srv.Dialer.Dial(dest)
	if err != nil {
		return &dialError{err}
	}
	mfd := newMeteredConn(fd, false)
	return srv.SetupConn(mfd, t.flags, dest)
}

func (t *dialTask) String() string {
	id := t.dest.ID()
	return fmt.Sprintf("%v %x %v:%d", t.flags, id[:8], t.dest.IP(), t.dest.TCP())
}

func (t *discoverTask) Do(srv *Server) {
	next := srv.lastLookup.Add(lookupInterval)
	if now := time.Now(); now.Before(next) {
		time.Sleep(next.Sub(now))
	}
	srv.lastLookup = time.Now()
	t.results = srv.ntab.LookupRandom()
}

func (t *discoverTask) String() string {
	s := "discovery lookup"
	if len(t.results) > 0 {
		s += fmt.Sprintf(" (%d results)", len(t.results))
	}
	return s
}

func (t waitExpireTask) Do(*Server) {
	time.Sleep(t.Duration)
}
func (t waitExpireTask) String() string {
	return fmt.Sprintf("wait for dial hist expire (%v)", t.Duration)
}

func (h dialHistory) min() pastDial {
	return h[0]
}
func (h *dialHistory) add(id cnode.ID, exp time.Time) {
	heap.Push(h, pastDial{id, exp})

}
func (h *dialHistory) remove(id cnode.ID) bool {
	for i, v := range *h {
		if v.id == id {
			heap.Remove(h, i)
			return true
		}
	}
	return false
}
func (h dialHistory) contains(id cnode.ID) bool {
	for _, v := range h {
		if v.id == id {
			return true
		}
	}
	return false
}
func (h *dialHistory) expire(now time.Time) {
	for h.Len() > 0 && h.min().exp.Before(now) {
		heap.Pop(h)
	}
}

func (h dialHistory) Len() int           { return len(h) }
func (h dialHistory) Less(i, j int) bool { return h[i].exp.Before(h[j].exp) }
func (h dialHistory) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *dialHistory) Push(x interface{}) {
	*h = append(*h, x.(pastDial))
}
func (h *dialHistory) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
