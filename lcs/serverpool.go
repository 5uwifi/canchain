package lcs

import (
	"crypto/ecdsa"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common/mclock"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/discv5"
	"github.com/5uwifi/canchain/lib/rlp"
)

const (
	shortRetryCnt       = 5
	shortRetryDelay     = time.Second * 5
	longRetryDelay      = time.Minute * 10
	maxNewEntries       = 1000
	maxKnownEntries     = 1000
	targetServerCount   = 5
	targetKnownSelect   = 3
	dialTimeout         = time.Second * 30
	targetConnTime      = time.Minute * 10
	discoverExpireStart = time.Minute * 20
	discoverExpireConst = time.Minute * 20
	failDropLn          = 0.1
	pstatReturnToMeanTC = time.Hour
	addrFailDropLn      = math.Ln2
	responseScoreTC     = time.Millisecond * 100
	delayScoreTC        = time.Second * 5
	timeoutPow          = 10
	initStatsWeight     = 1
)

type connReq struct {
	p      *peer
	node   *cnode.Node
	result chan *poolEntry
}

type disconnReq struct {
	entry   *poolEntry
	stopped bool
	done    chan struct{}
}

type registerReq struct {
	entry *poolEntry
	done  chan struct{}
}

type serverPool struct {
	db     candb.Database
	dbKey  []byte
	server *p2p.Server
	quit   chan struct{}
	wg     *sync.WaitGroup
	connWg sync.WaitGroup

	topic discv5.Topic

	discSetPeriod chan time.Duration
	discNodes     chan *cnode.Node
	discLookups   chan bool

	entries              map[cnode.ID]*poolEntry
	timeout, enableRetry chan *poolEntry
	adjustStats          chan poolStatAdjust

	connCh     chan *connReq
	disconnCh  chan *disconnReq
	registerCh chan *registerReq

	knownQueue, newQueue       poolEntryQueue
	knownSelect, newSelect     *weightedRandomSelect
	knownSelected, newSelected int
	fastDiscover               bool
}

func newServerPool(db candb.Database, quit chan struct{}, wg *sync.WaitGroup) *serverPool {
	pool := &serverPool{
		db:           db,
		quit:         quit,
		wg:           wg,
		entries:      make(map[cnode.ID]*poolEntry),
		timeout:      make(chan *poolEntry, 1),
		adjustStats:  make(chan poolStatAdjust, 100),
		enableRetry:  make(chan *poolEntry, 1),
		connCh:       make(chan *connReq),
		disconnCh:    make(chan *disconnReq),
		registerCh:   make(chan *registerReq),
		knownSelect:  newWeightedRandomSelect(),
		newSelect:    newWeightedRandomSelect(),
		fastDiscover: true,
	}
	pool.knownQueue = newPoolEntryQueue(maxKnownEntries, pool.removeEntry)
	pool.newQueue = newPoolEntryQueue(maxNewEntries, pool.removeEntry)
	return pool
}

func (pool *serverPool) start(server *p2p.Server, topic discv5.Topic) {
	pool.server = server
	pool.topic = topic
	pool.dbKey = append([]byte("serverPool/"), []byte(topic)...)
	pool.wg.Add(1)
	pool.loadNodes()

	if pool.server.DiscV5 != nil {
		pool.discSetPeriod = make(chan time.Duration, 1)
		pool.discNodes = make(chan *cnode.Node, 100)
		pool.discLookups = make(chan bool, 100)
		go pool.discoverNodes()
	}
	pool.checkDial()
	go pool.eventLoop()
}

func (pool *serverPool) discoverNodes() {
	ch := make(chan *discv5.Node)
	go func() {
		pool.server.DiscV5.SearchTopic(pool.topic, pool.discSetPeriod, ch, pool.discLookups)
		close(ch)
	}()
	for n := range ch {
		pubkey, err := decodePubkey64(n.ID[:])
		if err != nil {
			continue
		}
		pool.discNodes <- cnode.NewV4(pubkey, n.IP, int(n.TCP), int(n.UDP))
	}
}

func (pool *serverPool) connect(p *peer, node *cnode.Node) *poolEntry {
	log4j.Debug("Connect new entry", "ccnode", p.id)
	req := &connReq{p: p, node: node, result: make(chan *poolEntry, 1)}
	select {
	case pool.connCh <- req:
	case <-pool.quit:
		return nil
	}
	return <-req.result
}

func (pool *serverPool) registered(entry *poolEntry) {
	log4j.Debug("Registered new entry", "ccnode", entry.node.ID())
	req := &registerReq{entry: entry, done: make(chan struct{})}
	select {
	case pool.registerCh <- req:
	case <-pool.quit:
		return
	}
	<-req.done
}

func (pool *serverPool) disconnect(entry *poolEntry) {
	stopped := false
	select {
	case <-pool.quit:
		stopped = true
	default:
	}
	log4j.Debug("Disconnected old entry", "ccnode", entry.node.ID())
	req := &disconnReq{entry: entry, stopped: stopped, done: make(chan struct{})}

	pool.disconnCh <- req
	<-req.done
}

const (
	pseBlockDelay = iota
	pseResponseTime
	pseResponseTimeout
)

type poolStatAdjust struct {
	adjustType int
	entry      *poolEntry
	time       time.Duration
}

func (pool *serverPool) adjustBlockDelay(entry *poolEntry, time time.Duration) {
	if entry == nil {
		return
	}
	pool.adjustStats <- poolStatAdjust{pseBlockDelay, entry, time}
}

func (pool *serverPool) adjustResponseTime(entry *poolEntry, time time.Duration, timeout bool) {
	if entry == nil {
		return
	}
	if timeout {
		pool.adjustStats <- poolStatAdjust{pseResponseTimeout, entry, time}
	} else {
		pool.adjustStats <- poolStatAdjust{pseResponseTime, entry, time}
	}
}

func (pool *serverPool) eventLoop() {
	lookupCnt := 0
	var convTime mclock.AbsTime
	if pool.discSetPeriod != nil {
		pool.discSetPeriod <- time.Millisecond * 100
	}

	disconnect := func(req *disconnReq, stopped bool) {
		entry := req.entry
		if entry.state == psRegistered {
			connAdjust := float64(mclock.Now()-entry.regTime) / float64(targetConnTime)
			if connAdjust > 1 {
				connAdjust = 1
			}
			if stopped {
				entry.connectStats.add(1, connAdjust)
			} else {
				entry.connectStats.add(connAdjust, 1)
			}
		}
		entry.state = psNotConnected

		if entry.knownSelected {
			pool.knownSelected--
		} else {
			pool.newSelected--
		}
		pool.setRetryDial(entry)
		pool.connWg.Done()
		close(req.done)
	}

	for {
		select {
		case entry := <-pool.timeout:
			if !entry.removed {
				pool.checkDialTimeout(entry)
			}

		case entry := <-pool.enableRetry:
			if !entry.removed {
				entry.delayedRetry = false
				pool.updateCheckDial(entry)
			}

		case adj := <-pool.adjustStats:
			switch adj.adjustType {
			case pseBlockDelay:
				adj.entry.delayStats.add(float64(adj.time), 1)
			case pseResponseTime:
				adj.entry.responseStats.add(float64(adj.time), 1)
				adj.entry.timeoutStats.add(0, 1)
			case pseResponseTimeout:
				adj.entry.timeoutStats.add(1, 1)
			}

		case node := <-pool.discNodes:
			entry := pool.findOrNewNode(node)
			pool.updateCheckDial(entry)

		case conv := <-pool.discLookups:
			if conv {
				if lookupCnt == 0 {
					convTime = mclock.Now()
				}
				lookupCnt++
				if pool.fastDiscover && (lookupCnt == 50 || time.Duration(mclock.Now()-convTime) > time.Minute) {
					pool.fastDiscover = false
					if pool.discSetPeriod != nil {
						pool.discSetPeriod <- time.Minute
					}
				}
			}

		case req := <-pool.connCh:
			entry := pool.entries[req.p.ID()]
			if entry == nil {
				entry = pool.findOrNewNode(req.node)
			}
			if entry.state == psConnected || entry.state == psRegistered {
				req.result <- nil
				continue
			}
			pool.connWg.Add(1)
			entry.peer = req.p
			entry.state = psConnected
			addr := &poolEntryAddress{
				ip:       req.node.IP(),
				port:     uint16(req.node.TCP()),
				lastSeen: mclock.Now(),
			}
			entry.lastConnected = addr
			entry.addr = make(map[string]*poolEntryAddress)
			entry.addr[addr.strKey()] = addr
			entry.addrSelect = *newWeightedRandomSelect()
			entry.addrSelect.update(addr)
			req.result <- entry

		case req := <-pool.registerCh:
			entry := req.entry
			entry.state = psRegistered
			entry.regTime = mclock.Now()
			if !entry.known {
				pool.newQueue.remove(entry)
				entry.known = true
			}
			pool.knownQueue.setLatest(entry)
			entry.shortRetry = shortRetryCnt
			close(req.done)

		case req := <-pool.disconnCh:
			disconnect(req, req.stopped)

		case <-pool.quit:
			if pool.discSetPeriod != nil {
				close(pool.discSetPeriod)
			}

			go func() {
				pool.connWg.Wait()
				close(pool.disconnCh)
			}()

			for req := range pool.disconnCh {
				disconnect(req, true)
			}
			pool.saveNodes()
			pool.wg.Done()
			return
		}
	}
}

func (pool *serverPool) findOrNewNode(node *cnode.Node) *poolEntry {
	now := mclock.Now()
	entry := pool.entries[node.ID()]
	if entry == nil {
		log4j.Debug("Discovered new entry", "id", node.ID())
		entry = &poolEntry{
			node:       node,
			addr:       make(map[string]*poolEntryAddress),
			addrSelect: *newWeightedRandomSelect(),
			shortRetry: shortRetryCnt,
		}
		pool.entries[node.ID()] = entry
		entry.connectStats.add(1, initStatsWeight)
		entry.delayStats.add(0, initStatsWeight)
		entry.responseStats.add(0, initStatsWeight)
		entry.timeoutStats.add(0, initStatsWeight)
	}
	entry.lastDiscovered = now
	addr := &poolEntryAddress{ip: node.IP(), port: uint16(node.TCP())}
	if a, ok := entry.addr[addr.strKey()]; ok {
		addr = a
	} else {
		entry.addr[addr.strKey()] = addr
	}
	addr.lastSeen = now
	entry.addrSelect.update(addr)
	if !entry.known {
		pool.newQueue.setLatest(entry)
	}
	return entry
}

func (pool *serverPool) loadNodes() {
	enc, err := pool.db.Get(pool.dbKey)
	if err != nil {
		return
	}
	var list []*poolEntry
	err = rlp.DecodeBytes(enc, &list)
	if err != nil {
		log4j.Debug("Failed to decode node list", "err", err)
		return
	}
	for _, e := range list {
		log4j.Debug("Loaded server stats", "id", e.node.ID(), "fails", e.lastConnected.fails,
			"conn", fmt.Sprintf("%v/%v", e.connectStats.avg, e.connectStats.weight),
			"delay", fmt.Sprintf("%v/%v", time.Duration(e.delayStats.avg), e.delayStats.weight),
			"response", fmt.Sprintf("%v/%v", time.Duration(e.responseStats.avg), e.responseStats.weight),
			"timeout", fmt.Sprintf("%v/%v", e.timeoutStats.avg, e.timeoutStats.weight))
		pool.entries[e.node.ID()] = e
		pool.knownQueue.setLatest(e)
		pool.knownSelect.update((*knownEntry)(e))
	}
}

func (pool *serverPool) saveNodes() {
	list := make([]*poolEntry, len(pool.knownQueue.queue))
	for i := range list {
		list[i] = pool.knownQueue.fetchOldest()
	}
	enc, err := rlp.EncodeToBytes(list)
	if err == nil {
		pool.db.Put(pool.dbKey, enc)
	}
}

func (pool *serverPool) removeEntry(entry *poolEntry) {
	pool.newSelect.remove((*discoveredEntry)(entry))
	pool.knownSelect.remove((*knownEntry)(entry))
	entry.removed = true
	delete(pool.entries, entry.node.ID())
}

func (pool *serverPool) setRetryDial(entry *poolEntry) {
	delay := longRetryDelay
	if entry.shortRetry > 0 {
		entry.shortRetry--
		delay = shortRetryDelay
	}
	delay += time.Duration(rand.Int63n(int64(delay) + 1))
	entry.delayedRetry = true
	go func() {
		select {
		case <-pool.quit:
		case <-time.After(delay):
			select {
			case <-pool.quit:
			case pool.enableRetry <- entry:
			}
		}
	}()
}

func (pool *serverPool) updateCheckDial(entry *poolEntry) {
	pool.newSelect.update((*discoveredEntry)(entry))
	pool.knownSelect.update((*knownEntry)(entry))
	pool.checkDial()
}

func (pool *serverPool) checkDial() {
	fillWithKnownSelects := !pool.fastDiscover
	for pool.knownSelected < targetKnownSelect {
		entry := pool.knownSelect.choose()
		if entry == nil {
			fillWithKnownSelects = false
			break
		}
		pool.dial((*poolEntry)(entry.(*knownEntry)), true)
	}
	for pool.knownSelected+pool.newSelected < targetServerCount {
		entry := pool.newSelect.choose()
		if entry == nil {
			break
		}
		pool.dial((*poolEntry)(entry.(*discoveredEntry)), false)
	}
	if fillWithKnownSelects {
		for pool.knownSelected < targetServerCount {
			entry := pool.knownSelect.choose()
			if entry == nil {
				break
			}
			pool.dial((*poolEntry)(entry.(*knownEntry)), true)
		}
	}
}

func (pool *serverPool) dial(entry *poolEntry, knownSelected bool) {
	if pool.server == nil || entry.state != psNotConnected {
		return
	}
	entry.state = psDialed
	entry.knownSelected = knownSelected
	if knownSelected {
		pool.knownSelected++
	} else {
		pool.newSelected++
	}
	addr := entry.addrSelect.choose().(*poolEntryAddress)
	log4j.Debug("Dialing new peer", "lesaddr", entry.node.ID().String()+"@"+addr.strKey(), "set", len(entry.addr), "known", knownSelected)
	entry.dialed = addr
	go func() {
		pool.server.AddPeer(entry.node)
		select {
		case <-pool.quit:
		case <-time.After(dialTimeout):
			select {
			case <-pool.quit:
			case pool.timeout <- entry:
			}
		}
	}()
}

func (pool *serverPool) checkDialTimeout(entry *poolEntry) {
	if entry.state != psDialed {
		return
	}
	log4j.Debug("Dial timeout", "lesaddr", entry.node.ID().String()+"@"+entry.dialed.strKey())
	entry.state = psNotConnected
	if entry.knownSelected {
		pool.knownSelected--
	} else {
		pool.newSelected--
	}
	entry.connectStats.add(0, 1)
	entry.dialed.fails++
	pool.setRetryDial(entry)
}

const (
	psNotConnected = iota
	psDialed
	psConnected
	psRegistered
)

type poolEntry struct {
	peer                  *peer
	pubkey                [64]byte
	addr                  map[string]*poolEntryAddress
	node                  *cnode.Node
	lastConnected, dialed *poolEntryAddress
	addrSelect            weightedRandomSelect

	lastDiscovered              mclock.AbsTime
	known, knownSelected        bool
	connectStats, delayStats    poolStats
	responseStats, timeoutStats poolStats
	state                       int
	regTime                     mclock.AbsTime
	queueIdx                    int
	removed                     bool

	delayedRetry bool
	shortRetry   int
}

type poolEntryEnc struct {
	Pubkey                     []byte
	IP                         net.IP
	Port                       uint16
	Fails                      uint
	CStat, DStat, RStat, TStat poolStats
}

func (e *poolEntry) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, &poolEntryEnc{
		Pubkey: encodePubkey64(e.node.Pubkey()),
		IP:     e.lastConnected.ip,
		Port:   e.lastConnected.port,
		Fails:  e.lastConnected.fails,
		CStat:  e.connectStats,
		DStat:  e.delayStats,
		RStat:  e.responseStats,
		TStat:  e.timeoutStats,
	})
}

func (e *poolEntry) DecodeRLP(s *rlp.Stream) error {
	var entry poolEntryEnc
	if err := s.Decode(&entry); err != nil {
		return err
	}
	pubkey, err := decodePubkey64(entry.Pubkey)
	if err != nil {
		return err
	}
	addr := &poolEntryAddress{ip: entry.IP, port: entry.Port, fails: entry.Fails, lastSeen: mclock.Now()}
	e.node = cnode.NewV4(pubkey, entry.IP, int(entry.Port), int(entry.Port))
	e.addr = make(map[string]*poolEntryAddress)
	e.addr[addr.strKey()] = addr
	e.addrSelect = *newWeightedRandomSelect()
	e.addrSelect.update(addr)
	e.lastConnected = addr
	e.connectStats = entry.CStat
	e.delayStats = entry.DStat
	e.responseStats = entry.RStat
	e.timeoutStats = entry.TStat
	e.shortRetry = shortRetryCnt
	e.known = true
	return nil
}

func encodePubkey64(pub *ecdsa.PublicKey) []byte {
	return crypto.FromECDSAPub(pub)[:1]
}

func decodePubkey64(b []byte) (*ecdsa.PublicKey, error) {
	return crypto.UnmarshalPubkey(append([]byte{0x04}, b...))
}

type discoveredEntry poolEntry

func (e *discoveredEntry) Weight() int64 {
	if e.state != psNotConnected || e.delayedRetry {
		return 0
	}
	t := time.Duration(mclock.Now() - e.lastDiscovered)
	if t <= discoverExpireStart {
		return 1000000000
	}
	return int64(1000000000 * math.Exp(-float64(t-discoverExpireStart)/float64(discoverExpireConst)))
}

type knownEntry poolEntry

func (e *knownEntry) Weight() int64 {
	if e.state != psNotConnected || !e.known || e.delayedRetry {
		return 0
	}
	return int64(1000000000 * e.connectStats.recentAvg() * math.Exp(-float64(e.lastConnected.fails)*failDropLn-e.responseStats.recentAvg()/float64(responseScoreTC)-e.delayStats.recentAvg()/float64(delayScoreTC)) * math.Pow(1-e.timeoutStats.recentAvg(), timeoutPow))
}

type poolEntryAddress struct {
	ip       net.IP
	port     uint16
	lastSeen mclock.AbsTime
	fails    uint
}

func (a *poolEntryAddress) Weight() int64 {
	t := time.Duration(mclock.Now() - a.lastSeen)
	return int64(1000000*math.Exp(-float64(t)/float64(discoverExpireConst)-float64(a.fails)*addrFailDropLn)) + 1
}

func (a *poolEntryAddress) strKey() string {
	return a.ip.String() + ":" + strconv.Itoa(int(a.port))
}

type poolStats struct {
	sum, weight, avg, recent float64
	lastRecalc               mclock.AbsTime
}

func (s *poolStats) init(sum, weight float64) {
	s.sum = sum
	s.weight = weight
	var avg float64
	if weight > 0 {
		avg = s.sum / weight
	}
	s.avg = avg
	s.recent = avg
	s.lastRecalc = mclock.Now()
}

func (s *poolStats) recalc() {
	now := mclock.Now()
	s.recent = s.avg + (s.recent-s.avg)*math.Exp(-float64(now-s.lastRecalc)/float64(pstatReturnToMeanTC))
	if s.sum == 0 {
		s.avg = 0
	} else {
		if s.sum > s.weight*1e30 {
			s.avg = 1e30
		} else {
			s.avg = s.sum / s.weight
		}
	}
	s.lastRecalc = now
}

func (s *poolStats) add(value, weight float64) {
	s.weight += weight
	s.sum += value * weight
	s.recalc()
}

func (s *poolStats) recentAvg() float64 {
	s.recalc()
	return s.recent
}

func (s *poolStats) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, []interface{}{math.Float64bits(s.sum), math.Float64bits(s.weight)})
}

func (s *poolStats) DecodeRLP(st *rlp.Stream) error {
	var stats struct {
		SumUint, WeightUint uint64
	}
	if err := st.Decode(&stats); err != nil {
		return err
	}
	s.init(math.Float64frombits(stats.SumUint), math.Float64frombits(stats.WeightUint))
	return nil
}

type poolEntryQueue struct {
	queue                  map[int]*poolEntry
	newPtr, oldPtr, maxCnt int
	removeFromPool         func(*poolEntry)
}

func newPoolEntryQueue(maxCnt int, removeFromPool func(*poolEntry)) poolEntryQueue {
	return poolEntryQueue{queue: make(map[int]*poolEntry), maxCnt: maxCnt, removeFromPool: removeFromPool}
}

func (q *poolEntryQueue) fetchOldest() *poolEntry {
	if len(q.queue) == 0 {
		return nil
	}
	for {
		if e := q.queue[q.oldPtr]; e != nil {
			delete(q.queue, q.oldPtr)
			q.oldPtr++
			return e
		}
		q.oldPtr++
	}
}

func (q *poolEntryQueue) remove(entry *poolEntry) {
	if q.queue[entry.queueIdx] == entry {
		delete(q.queue, entry.queueIdx)
	}
}

func (q *poolEntryQueue) setLatest(entry *poolEntry) {
	if q.queue[entry.queueIdx] == entry {
		delete(q.queue, entry.queueIdx)
	} else {
		if len(q.queue) == q.maxCnt {
			e := q.fetchOldest()
			q.remove(e)
			q.removeFromPool(e)
		}
	}
	entry.queueIdx = q.newPtr
	q.queue[entry.queueIdx] = entry
	q.newPtr++
}
