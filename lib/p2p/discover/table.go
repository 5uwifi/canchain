package discover

import (
	"crypto/ecdsa"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/netutil"
)

const (
	alpha           = 3
	bucketSize      = 16
	maxReplacements = 10

	hashBits          = len(common.Hash{}) * 8
	nBuckets          = hashBits / 15
	bucketMinDistance = hashBits - nBuckets

	bucketIPLimit, bucketSubnet = 2, 24
	tableIPLimit, tableSubnet   = 10, 24

	maxFindnodeFailures = 5
	refreshInterval     = 30 * time.Minute
	revalidateInterval  = 10 * time.Second
	copyNodesInterval   = 30 * time.Second
	seedMinTableTime    = 5 * time.Minute
	seedCount           = 30
	seedMaxAge          = 5 * 24 * time.Hour
)

type Table struct {
	mutex   sync.Mutex
	buckets [nBuckets]*bucket
	nursery []*node
	rand    *mrand.Rand
	ips     netutil.DistinctNetSet

	db         *cnode.DB
	refreshReq chan chan struct{}
	initDone   chan struct{}
	closeReq   chan struct{}
	closed     chan struct{}

	nodeAddedHook func(*node)

	net  transport
	self *node
}

type transport interface {
	ping(cnode.ID, *net.UDPAddr) error
	findnode(toid cnode.ID, addr *net.UDPAddr, target encPubkey) ([]*node, error)
	close()
}

type bucket struct {
	entries      []*node
	replacements []*node
	ips          netutil.DistinctNetSet
}

func newTable(t transport, self *cnode.Node, db *cnode.DB, bootnodes []*cnode.Node) (*Table, error) {
	tab := &Table{
		net:        t,
		db:         db,
		self:       wrapNode(self),
		refreshReq: make(chan chan struct{}),
		initDone:   make(chan struct{}),
		closeReq:   make(chan struct{}),
		closed:     make(chan struct{}),
		rand:       mrand.New(mrand.NewSource(0)),
		ips:        netutil.DistinctNetSet{Subnet: tableSubnet, Limit: tableIPLimit},
	}
	if err := tab.setFallbackNodes(bootnodes); err != nil {
		return nil, err
	}
	for i := range tab.buckets {
		tab.buckets[i] = &bucket{
			ips: netutil.DistinctNetSet{Subnet: bucketSubnet, Limit: bucketIPLimit},
		}
	}
	tab.seedRand()
	tab.loadSeedNodes()

	go tab.loop()
	return tab, nil
}

func (tab *Table) seedRand() {
	var b [8]byte
	crand.Read(b[:])

	tab.mutex.Lock()
	tab.rand.Seed(int64(binary.BigEndian.Uint64(b[:])))
	tab.mutex.Unlock()
}

func (tab *Table) Self() *cnode.Node {
	return unwrapNode(tab.self)
}

func (tab *Table) ReadRandomNodes(buf []*cnode.Node) (n int) {
	if !tab.isInitDone() {
		return 0
	}
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	var buckets [][]*node
	for _, b := range &tab.buckets {
		if len(b.entries) > 0 {
			buckets = append(buckets, b.entries)
		}
	}
	if len(buckets) == 0 {
		return 0
	}
	for i := len(buckets) - 1; i > 0; i-- {
		j := tab.rand.Intn(len(buckets))
		buckets[i], buckets[j] = buckets[j], buckets[i]
	}
	var i, j int
	for ; i < len(buf); i, j = i+1, (j+1)%len(buckets) {
		b := buckets[j]
		buf[i] = unwrapNode(b[0])
		buckets[j] = b[1:]
		if len(b) == 1 {
			buckets = append(buckets[:j], buckets[j+1:]...)
		}
		if len(buckets) == 0 {
			break
		}
	}
	return i + 1
}

func (tab *Table) Close() {
	select {
	case <-tab.closed:
	case tab.closeReq <- struct{}{}:
		<-tab.closed
	}
}

func (tab *Table) setFallbackNodes(nodes []*cnode.Node) error {
	for _, n := range nodes {
		if err := n.ValidateComplete(); err != nil {
			return fmt.Errorf("bad bootstrap node %q: %v", n, err)
		}
	}
	tab.nursery = wrapNodes(nodes)
	return nil
}

func (tab *Table) isInitDone() bool {
	select {
	case <-tab.initDone:
		return true
	default:
		return false
	}
}

func (tab *Table) Resolve(n *cnode.Node) *cnode.Node {
	hash := n.ID()
	tab.mutex.Lock()
	cl := tab.closest(hash, 1)
	tab.mutex.Unlock()
	if len(cl.entries) > 0 && cl.entries[0].ID() == hash {
		return unwrapNode(cl.entries[0])
	}
	result := tab.lookup(encodePubkey(n.Pubkey()), true)
	for _, n := range result {
		if n.ID() == hash {
			return unwrapNode(n)
		}
	}
	return nil
}

func (tab *Table) LookupRandom() []*cnode.Node {
	var target encPubkey
	crand.Read(target[:])
	return unwrapNodes(tab.lookup(target, true))
}

func (tab *Table) lookup(targetKey encPubkey, refreshIfEmpty bool) []*node {
	var (
		target         = cnode.ID(crypto.Keccak256Hash(targetKey[:]))
		asked          = make(map[cnode.ID]bool)
		seen           = make(map[cnode.ID]bool)
		reply          = make(chan []*node, alpha)
		pendingQueries = 0
		result         *nodesByDistance
	)
	asked[tab.self.ID()] = true

	for {
		tab.mutex.Lock()
		result = tab.closest(target, bucketSize)
		tab.mutex.Unlock()
		if len(result.entries) > 0 || !refreshIfEmpty {
			break
		}
		<-tab.refresh()
		refreshIfEmpty = false
	}

	for {
		for i := 0; i < len(result.entries) && pendingQueries < alpha; i++ {
			n := result.entries[i]
			if !asked[n.ID()] {
				asked[n.ID()] = true
				pendingQueries++
				go tab.findnode(n, targetKey, reply)
			}
		}
		if pendingQueries == 0 {
			break
		}
		for _, n := range <-reply {
			if n != nil && !seen[n.ID()] {
				seen[n.ID()] = true
				result.push(n, bucketSize)
			}
		}
		pendingQueries--
	}
	return result.entries
}

func (tab *Table) findnode(n *node, targetKey encPubkey, reply chan<- []*node) {
	fails := tab.db.FindFails(n.ID())
	r, err := tab.net.findnode(n.ID(), n.addr(), targetKey)
	if err != nil || len(r) == 0 {
		fails++
		tab.db.UpdateFindFails(n.ID(), fails)
		log4j.Trace("Findnode failed", "id", n.ID(), "failcount", fails, "err", err)
		if fails >= maxFindnodeFailures {
			log4j.Trace("Too many findnode failures, dropping", "id", n.ID(), "failcount", fails)
			tab.delete(n)
		}
	} else if fails > 0 {
		tab.db.UpdateFindFails(n.ID(), fails-1)
	}

	for _, n := range r {
		tab.add(n)
	}
	reply <- r
}

func (tab *Table) refresh() <-chan struct{} {
	done := make(chan struct{})
	select {
	case tab.refreshReq <- done:
	case <-tab.closed:
		close(done)
	}
	return done
}

func (tab *Table) loop() {
	var (
		revalidate     = time.NewTimer(tab.nextRevalidateTime())
		refresh        = time.NewTicker(refreshInterval)
		copyNodes      = time.NewTicker(copyNodesInterval)
		revalidateDone = make(chan struct{})
		refreshDone    = make(chan struct{})
		waiting        = []chan struct{}{tab.initDone}
	)
	defer refresh.Stop()
	defer revalidate.Stop()
	defer copyNodes.Stop()

	go tab.doRefresh(refreshDone)

loop:
	for {
		select {
		case <-refresh.C:
			tab.seedRand()
			if refreshDone == nil {
				refreshDone = make(chan struct{})
				go tab.doRefresh(refreshDone)
			}
		case req := <-tab.refreshReq:
			waiting = append(waiting, req)
			if refreshDone == nil {
				refreshDone = make(chan struct{})
				go tab.doRefresh(refreshDone)
			}
		case <-refreshDone:
			for _, ch := range waiting {
				close(ch)
			}
			waiting, refreshDone = nil, nil
		case <-revalidate.C:
			go tab.doRevalidate(revalidateDone)
		case <-revalidateDone:
			revalidate.Reset(tab.nextRevalidateTime())
		case <-copyNodes.C:
			go tab.copyLiveNodes()
		case <-tab.closeReq:
			break loop
		}
	}

	if tab.net != nil {
		tab.net.close()
	}
	if refreshDone != nil {
		<-refreshDone
	}
	for _, ch := range waiting {
		close(ch)
	}
	close(tab.closed)
}

func (tab *Table) doRefresh(done chan struct{}) {
	defer close(done)

	tab.loadSeedNodes()

	var key ecdsa.PublicKey
	if err := tab.self.Load((*cnode.Secp256k1)(&key)); err == nil {
		tab.lookup(encodePubkey(&key), false)
	}

	for i := 0; i < 3; i++ {
		var target encPubkey
		crand.Read(target[:])
		tab.lookup(target, false)
	}
}

func (tab *Table) loadSeedNodes() {
	seeds := wrapNodes(tab.db.QuerySeeds(seedCount, seedMaxAge))
	seeds = append(seeds, tab.nursery...)
	for i := range seeds {
		seed := seeds[i]
		age := log4j.Lazy{Fn: func() interface{} { return time.Since(tab.db.LastPongReceived(seed.ID())) }}
		log4j.Debug("Found seed node in database", "id", seed.ID(), "addr", seed.addr(), "age", age)
		tab.add(seed)
	}
}

func (tab *Table) doRevalidate(done chan<- struct{}) {
	defer func() { done <- struct{}{} }()

	last, bi := tab.nodeToRevalidate()
	if last == nil {
		return
	}

	err := tab.net.ping(last.ID(), last.addr())

	tab.mutex.Lock()
	defer tab.mutex.Unlock()
	b := tab.buckets[bi]
	if err == nil {
		log4j.Debug("Revalidated node", "b", bi, "id", last.ID())
		b.bump(last)
		return
	}
	if r := tab.replace(b, last); r != nil {
		log4j.Debug("Replaced dead node", "b", bi, "id", last.ID(), "ip", last.IP(), "r", r.ID(), "rip", r.IP())
	} else {
		log4j.Debug("Removed dead node", "b", bi, "id", last.ID(), "ip", last.IP())
	}
}

func (tab *Table) nodeToRevalidate() (n *node, bi int) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	for _, bi = range tab.rand.Perm(len(tab.buckets)) {
		b := tab.buckets[bi]
		if len(b.entries) > 0 {
			last := b.entries[len(b.entries)-1]
			return last, bi
		}
	}
	return nil, 0
}

func (tab *Table) nextRevalidateTime() time.Duration {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	return time.Duration(tab.rand.Int63n(int64(revalidateInterval)))
}

func (tab *Table) copyLiveNodes() {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	now := time.Now()
	for _, b := range &tab.buckets {
		for _, n := range b.entries {
			if now.Sub(n.addedAt) >= seedMinTableTime {
				tab.db.UpdateNode(unwrapNode(n))
			}
		}
	}
}

func (tab *Table) closest(target cnode.ID, nresults int) *nodesByDistance {
	close := &nodesByDistance{target: target}
	for _, b := range &tab.buckets {
		for _, n := range b.entries {
			close.push(n, nresults)
		}
	}
	return close
}

func (tab *Table) len() (n int) {
	for _, b := range &tab.buckets {
		n += len(b.entries)
	}
	return n
}

func (tab *Table) bucket(id cnode.ID) *bucket {
	d := cnode.LogDist(tab.self.ID(), id)
	if d <= bucketMinDistance {
		return tab.buckets[0]
	}
	return tab.buckets[d-bucketMinDistance-1]
}

func (tab *Table) add(n *node) {
	if n.ID() == tab.self.ID() {
		return
	}

	tab.mutex.Lock()
	defer tab.mutex.Unlock()
	b := tab.bucket(n.ID())
	if !tab.bumpOrAdd(b, n) {
		tab.addReplacement(b, n)
	}
}

func (tab *Table) addThroughPing(n *node) {
	if !tab.isInitDone() {
		return
	}
	tab.add(n)
}

func (tab *Table) stuff(nodes []*node) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	for _, n := range nodes {
		if n.ID() == tab.self.ID() {
			continue
		}
		b := tab.bucket(n.ID())
		if len(b.entries) < bucketSize {
			tab.bumpOrAdd(b, n)
		}
	}
}

func (tab *Table) delete(node *node) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	tab.deleteInBucket(tab.bucket(node.ID()), node)
}

func (tab *Table) addIP(b *bucket, ip net.IP) bool {
	if netutil.IsLAN(ip) {
		return true
	}
	if !tab.ips.Add(ip) {
		log4j.Debug("IP exceeds table limit", "ip", ip)
		return false
	}
	if !b.ips.Add(ip) {
		log4j.Debug("IP exceeds bucket limit", "ip", ip)
		tab.ips.Remove(ip)
		return false
	}
	return true
}

func (tab *Table) removeIP(b *bucket, ip net.IP) {
	if netutil.IsLAN(ip) {
		return
	}
	tab.ips.Remove(ip)
	b.ips.Remove(ip)
}

func (tab *Table) addReplacement(b *bucket, n *node) {
	for _, e := range b.replacements {
		if e.ID() == n.ID() {
			return
		}
	}
	if !tab.addIP(b, n.IP()) {
		return
	}
	var removed *node
	b.replacements, removed = pushNode(b.replacements, n, maxReplacements)
	if removed != nil {
		tab.removeIP(b, removed.IP())
	}
}

func (tab *Table) replace(b *bucket, last *node) *node {
	if len(b.entries) == 0 || b.entries[len(b.entries)-1].ID() != last.ID() {
		return nil
	}
	if len(b.replacements) == 0 {
		tab.deleteInBucket(b, last)
		return nil
	}
	r := b.replacements[tab.rand.Intn(len(b.replacements))]
	b.replacements = deleteNode(b.replacements, r)
	b.entries[len(b.entries)-1] = r
	tab.removeIP(b, last.IP())
	return r
}

func (b *bucket) bump(n *node) bool {
	for i := range b.entries {
		if b.entries[i].ID() == n.ID() {
			copy(b.entries[1:], b.entries[:i])
			b.entries[0] = n
			return true
		}
	}
	return false
}

func (tab *Table) bumpOrAdd(b *bucket, n *node) bool {
	if b.bump(n) {
		return true
	}
	if len(b.entries) >= bucketSize || !tab.addIP(b, n.IP()) {
		return false
	}
	b.entries, _ = pushNode(b.entries, n, bucketSize)
	b.replacements = deleteNode(b.replacements, n)
	n.addedAt = time.Now()
	if tab.nodeAddedHook != nil {
		tab.nodeAddedHook(n)
	}
	return true
}

func (tab *Table) deleteInBucket(b *bucket, n *node) {
	b.entries = deleteNode(b.entries, n)
	tab.removeIP(b, n.IP())
}

func pushNode(list []*node, n *node, max int) ([]*node, *node) {
	if len(list) < max {
		list = append(list, nil)
	}
	removed := list[len(list)-1]
	copy(list[1:], list)
	list[0] = n
	return list, removed
}

func deleteNode(list []*node, n *node) []*node {
	for i := range list {
		if list[i].ID() == n.ID() {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

type nodesByDistance struct {
	entries []*node
	target  cnode.ID
}

func (h *nodesByDistance) push(n *node, maxElems int) {
	ix := sort.Search(len(h.entries), func(i int) bool {
		return cnode.DistCmp(h.target, h.entries[i].ID(), n.ID()) > 0
	})
	if len(h.entries) < maxElems {
		h.entries = append(h.entries, n)
	}
	if ix == len(h.entries) {
	} else {
		copy(h.entries[ix+1:], h.entries[ix:])
		h.entries[ix] = n
	}
}
