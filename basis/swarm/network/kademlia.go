
package network

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/pot"
)

/*

Taking the proximity order relative to a fix point x classifies the points in
the space (n byte long byte sequences) into bins. Items in each are at
most half as distant from x as items in the previous bin. Given a sample of
uniformly distributed items (a hash function over arbitrary sequence) the
proximity scale maps onto series of subsets with cardinalities on a negative
exponential scale.

It also has the property that any two item belonging to the same bin are at
most half as distant from each other as they are from x.

If we think of random sample of items in the bins as connections in a network of
interconnected nodes then relative proximity can serve as the basis for local
decisions for graph traversal where the task is to find a route between two
points. Since in every hop, the finite distance halves, there is
a guaranteed constant maximum limit on the number of hops needed to reach one
node from the other.
*/

var pof = pot.DefaultPof(256)

type KadParams struct {
	// adjustable parameters
	MaxProxDisplay int   // number of rows the table shows
	MinProxBinSize int   // nearest neighbour core minimum cardinality
	MinBinSize     int   // minimum number of peers in a row
	MaxBinSize     int   // maximum number of peers in a row before pruning
	RetryInterval  int64 // initial interval before a peer is first redialed
	RetryExponent  int   // exponent to multiply retry intervals with
	MaxRetries     int   // maximum number of redial attempts
	// function to sanction or prevent suggesting a peer
	Reachable func(OverlayAddr) bool
}

func NewKadParams() *KadParams {
	return &KadParams{
		MaxProxDisplay: 16,
		MinProxBinSize: 2,
		MinBinSize:     2,
		MaxBinSize:     4,
		RetryInterval:  4200000000, // 4.2 sec
		MaxRetries:     42,
		RetryExponent:  2,
	}
}

type Kademlia struct {
	lock       sync.RWMutex
	*KadParams          // Kademlia configuration parameters
	base       []byte   // immutable baseaddress of the table
	addrs      *pot.Pot // pots container for known peer addresses
	conns      *pot.Pot // pots container for live peer connections
	depth      uint8    // stores the last current depth of saturation
	nDepth     int      // stores the last neighbourhood depth
	nDepthC    chan int // returned by DepthC function to signal neighbourhood depth change
	addrCountC chan int // returned by AddrCountC function to signal peer count change
}

func NewKademlia(addr []byte, params *KadParams) *Kademlia {
	if params == nil {
		params = NewKadParams()
	}
	return &Kademlia{
		base:      addr,
		KadParams: params,
		addrs:     pot.NewPot(nil, 0),
		conns:     pot.NewPot(nil, 0),
	}
}

type OverlayPeer interface {
	Address() []byte
}

type OverlayConn interface {
	OverlayPeer
	Drop(error)       // call to indicate a peer should be expunged
	Off() OverlayAddr // call to return a persitent OverlayAddr
}

type OverlayAddr interface {
	OverlayPeer
	Update(OverlayAddr) OverlayAddr // returns the updated version of the original
}

type entry struct {
	OverlayPeer
	seenAt  time.Time
	retries int
}

func newEntry(p OverlayPeer) *entry {
	return &entry{
		OverlayPeer: p,
		seenAt:      time.Now(),
	}
}

func (e *entry) Bin() string {
	return pot.ToBin(e.addr().Address())
}

func Label(e *entry) string {
	return fmt.Sprintf("%s (%d)", e.Hex()[:4], e.retries)
}

func (e *entry) Hex() string {
	return fmt.Sprintf("%x", e.addr().Address())
}

func (e *entry) String() string {
	return fmt.Sprintf("%s (%d)", e.Hex()[:8], e.retries)
}

func (e *entry) addr() OverlayAddr {
	a, _ := e.OverlayPeer.(OverlayAddr)
	return a
}

func (e *entry) conn() OverlayConn {
	c, _ := e.OverlayPeer.(OverlayConn)
	return c
}

func (k *Kademlia) Register(peers []OverlayAddr) error {
	k.lock.Lock()
	defer k.lock.Unlock()
	var known, size int
	for _, p := range peers {
		// error if self received, peer should know better
		// and should be punished for this
		if bytes.Equal(p.Address(), k.base) {
			return fmt.Errorf("add peers: %x is self", k.base)
		}
		var found bool
		k.addrs, _, found, _ = pot.Swap(k.addrs, p, pof, func(v pot.Val) pot.Val {
			// if not found
			if v == nil {
				// insert new offline peer into conns
				return newEntry(p)
			}
			// found among known peers, do nothing
			return v
		})
		if found {
			known++
		}
		size++
	}
	// send new address count value only if there are new addresses
	if k.addrCountC != nil && size-known > 0 {
		k.addrCountC <- k.addrs.Size()
	}
	// log.Trace(fmt.Sprintf("%x registered %v peers, %v known, total: %v", k.BaseAddr()[:4], size, known, k.addrs.Size()))

	k.sendNeighbourhoodDepthChange()
	return nil
}

func (k *Kademlia) SuggestPeer() (a OverlayAddr, o int, want bool) {
	k.lock.Lock()
	defer k.lock.Unlock()
	minsize := k.MinBinSize
	depth := k.neighbourhoodDepth()
	// if there is a callable neighbour within the current proxBin, connect
	// this makes sure nearest neighbour set is fully connected
	var ppo int
	k.addrs.EachNeighbour(k.base, pof, func(val pot.Val, po int) bool {
		if po < depth {
			return false
		}
		a = k.callable(val)
		ppo = po
		return a == nil
	})
	if a != nil {
		log.Trace(fmt.Sprintf("%08x candidate nearest neighbour found: %v (%v)", k.BaseAddr()[:4], a, ppo))
		return a, 0, false
	}
	// log.Trace(fmt.Sprintf("%08x no candidate nearest neighbours to connect to (Depth: %v, minProxSize: %v) %#v", k.BaseAddr()[:4], depth, k.MinProxBinSize, a))

	var bpo []int
	prev := -1
	k.conns.EachBin(k.base, pof, 0, func(po, size int, f func(func(val pot.Val, i int) bool) bool) bool {
		prev++
		for ; prev < po; prev++ {
			bpo = append(bpo, prev)
			minsize = 0
		}
		if size < minsize {
			bpo = append(bpo, po)
			minsize = size
		}
		return size > 0 && po < depth
	})
	// all buckets are full, ie., minsize == k.MinBinSize
	if len(bpo) == 0 {
		// log.Debug(fmt.Sprintf("%08x: all bins saturated", k.BaseAddr()[:4]))
		return nil, 0, false
	}
	// as long as we got candidate peers to connect to
	// dont ask for new peers (want = false)
	// try to select a candidate peer
	// find the first callable peer
	nxt := bpo[0]
	k.addrs.EachBin(k.base, pof, nxt, func(po, _ int, f func(func(pot.Val, int) bool) bool) bool {
		// for each bin (up until depth) we find callable candidate peers
		if po >= depth {
			return false
		}
		return f(func(val pot.Val, _ int) bool {
			a = k.callable(val)
			return a == nil
		})
	})
	// found a candidate
	if a != nil {
		return a, 0, false
	}
	// no candidate peer found, request for the short bin
	var changed bool
	if uint8(nxt) < k.depth {
		k.depth = uint8(nxt)
		changed = true
	}
	return a, nxt, changed
}

func (k *Kademlia) On(p OverlayConn) (uint8, bool) {
	k.lock.Lock()
	defer k.lock.Unlock()
	e := newEntry(p)
	var ins bool
	k.conns, _, _, _ = pot.Swap(k.conns, p, pof, func(v pot.Val) pot.Val {
		// if not found live
		if v == nil {
			ins = true
			// insert new online peer into conns
			return e
		}
		// found among live peers, do nothing
		return v
	})
	if ins {
		// insert new online peer into addrs
		k.addrs, _, _, _ = pot.Swap(k.addrs, p, pof, func(v pot.Val) pot.Val {
			return e
		})
		// send new address count value only if the peer is inserted
		if k.addrCountC != nil {
			k.addrCountC <- k.addrs.Size()
		}
	}
	log.Trace(k.string())
	// calculate if depth of saturation changed
	depth := uint8(k.saturation(k.MinBinSize))
	var changed bool
	if depth != k.depth {
		changed = true
		k.depth = depth
	}
	k.sendNeighbourhoodDepthChange()
	return k.depth, changed
}

func (k *Kademlia) NeighbourhoodDepthC() <-chan int {
	if k.nDepthC == nil {
		k.nDepthC = make(chan int)
	}
	return k.nDepthC
}

func (k *Kademlia) sendNeighbourhoodDepthChange() {
	// nDepthC is initialized when NeighbourhoodDepthC is called and returned by it.
	// It provides signaling of neighbourhood depth change.
	// This part of the code is sending new neighbourhood depth to nDepthC if that condition is met.
	if k.nDepthC != nil {
		nDepth := k.neighbourhoodDepth()
		if nDepth != k.nDepth {
			k.nDepth = nDepth
			k.nDepthC <- nDepth
		}
	}
}

func (k *Kademlia) AddrCountC() <-chan int {
	if k.addrCountC == nil {
		k.addrCountC = make(chan int)
	}
	return k.addrCountC
}

func (k *Kademlia) Off(p OverlayConn) {
	k.lock.Lock()
	defer k.lock.Unlock()
	var del bool
	k.addrs, _, _, _ = pot.Swap(k.addrs, p, pof, func(v pot.Val) pot.Val {
		// v cannot be nil, must check otherwise we overwrite entry
		if v == nil {
			panic(fmt.Sprintf("connected peer not found %v", p))
		}
		del = true
		return newEntry(p.Off())
	})

	if del {
		k.conns, _, _, _ = pot.Swap(k.conns, p, pof, func(_ pot.Val) pot.Val {
			// v cannot be nil, but no need to check
			return nil
		})
		// send new address count value only if the peer is deleted
		if k.addrCountC != nil {
			k.addrCountC <- k.addrs.Size()
		}
		k.sendNeighbourhoodDepthChange()
	}
}

func (k *Kademlia) EachBin(base []byte, pof pot.Pof, o int, eachBinFunc func(conn OverlayConn, po int) bool) {
	k.lock.RLock()
	defer k.lock.RUnlock()

	var startPo int
	var endPo int
	kadDepth := k.neighbourhoodDepth()

	k.conns.EachBin(base, pof, o, func(po, size int, f func(func(val pot.Val, i int) bool) bool) bool {
		if startPo > 0 && endPo != k.MaxProxDisplay {
			startPo = endPo + 1
		}
		if po < kadDepth {
			endPo = po
		} else {
			endPo = k.MaxProxDisplay
		}

		for bin := startPo; bin <= endPo; bin++ {
			f(func(val pot.Val, _ int) bool {
				return eachBinFunc(val.(*entry).conn(), bin)
			})
		}
		return true
	})
}

func (k *Kademlia) EachConn(base []byte, o int, f func(OverlayConn, int, bool) bool) {
	k.lock.RLock()
	defer k.lock.RUnlock()
	k.eachConn(base, o, f)
}

func (k *Kademlia) eachConn(base []byte, o int, f func(OverlayConn, int, bool) bool) {
	if len(base) == 0 {
		base = k.base
	}
	depth := k.neighbourhoodDepth()
	k.conns.EachNeighbour(base, pof, func(val pot.Val, po int) bool {
		if po > o {
			return true
		}
		return f(val.(*entry).conn(), po, po >= depth)
	})
}

func (k *Kademlia) EachAddr(base []byte, o int, f func(OverlayAddr, int, bool) bool) {
	k.lock.RLock()
	defer k.lock.RUnlock()
	k.eachAddr(base, o, f)
}

func (k *Kademlia) eachAddr(base []byte, o int, f func(OverlayAddr, int, bool) bool) {
	if len(base) == 0 {
		base = k.base
	}
	depth := k.neighbourhoodDepth()
	k.addrs.EachNeighbour(base, pof, func(val pot.Val, po int) bool {
		if po > o {
			return true
		}
		return f(val.(*entry).addr(), po, po >= depth)
	})
}

func (k *Kademlia) neighbourhoodDepth() (depth int) {
	if k.conns.Size() < k.MinProxBinSize {
		return 0
	}
	var size int
	f := func(v pot.Val, i int) bool {
		size++
		depth = i
		return size < k.MinProxBinSize
	}
	k.conns.EachNeighbour(k.base, pof, f)
	return depth
}

func (k *Kademlia) callable(val pot.Val) OverlayAddr {
	e := val.(*entry)
	// not callable if peer is live or exceeded maxRetries
	if e.conn() != nil || e.retries > k.MaxRetries {
		return nil
	}
	// calculate the allowed number of retries based on time lapsed since last seen
	timeAgo := int64(time.Since(e.seenAt))
	div := int64(k.RetryExponent)
	div += (150000 - rand.Int63n(300000)) * div / 1000000
	var retries int
	for delta := timeAgo; delta > k.RetryInterval; delta /= div {
		retries++
	}
	// this is never called concurrently, so safe to increment
	// peer can be retried again
	if retries < e.retries {
		log.Trace(fmt.Sprintf("%08x: %v long time since last try (at %v) needed before retry %v, wait only warrants %v", k.BaseAddr()[:4], e, timeAgo, e.retries, retries))
		return nil
	}
	// function to sanction or prevent suggesting a peer
	if k.Reachable != nil && !k.Reachable(e.addr()) {
		log.Trace(fmt.Sprintf("%08x: peer %v is temporarily not callable", k.BaseAddr()[:4], e))
		return nil
	}
	e.retries++
	log.Trace(fmt.Sprintf("%08x: peer %v is callable", k.BaseAddr()[:4], e))

	return e.addr()
}

func (k *Kademlia) BaseAddr() []byte {
	return k.base
}

func (k *Kademlia) String() string {
	k.lock.RLock()
	defer k.lock.RUnlock()
	return k.string()
}

func (k *Kademlia) string() string {
	wsrow := "                          "
	var rows []string

	rows = append(rows, "=========================================================================")
	rows = append(rows, fmt.Sprintf("%v KΛÐΞMLIΛ hive: queen's address: %x", time.Now().UTC().Format(time.UnixDate), k.BaseAddr()[:3]))
	rows = append(rows, fmt.Sprintf("population: %d (%d), MinProxBinSize: %d, MinBinSize: %d, MaxBinSize: %d", k.conns.Size(), k.addrs.Size(), k.MinProxBinSize, k.MinBinSize, k.MaxBinSize))

	liverows := make([]string, k.MaxProxDisplay)
	peersrows := make([]string, k.MaxProxDisplay)

	depth := k.neighbourhoodDepth()
	rest := k.conns.Size()
	k.conns.EachBin(k.base, pof, 0, func(po, size int, f func(func(val pot.Val, i int) bool) bool) bool {
		var rowlen int
		if po >= k.MaxProxDisplay {
			po = k.MaxProxDisplay - 1
		}
		row := []string{fmt.Sprintf("%2d", size)}
		rest -= size
		f(func(val pot.Val, vpo int) bool {
			e := val.(*entry)
			row = append(row, fmt.Sprintf("%x", e.Address()[:2]))
			rowlen++
			return rowlen < 4
		})
		r := strings.Join(row, " ")
		r = r + wsrow
		liverows[po] = r[:31]
		return true
	})

	k.addrs.EachBin(k.base, pof, 0, func(po, size int, f func(func(val pot.Val, i int) bool) bool) bool {
		var rowlen int
		if po >= k.MaxProxDisplay {
			po = k.MaxProxDisplay - 1
		}
		if size < 0 {
			panic("wtf")
		}
		row := []string{fmt.Sprintf("%2d", size)}
		// we are displaying live peers too
		f(func(val pot.Val, vpo int) bool {
			e := val.(*entry)
			row = append(row, Label(e))
			rowlen++
			return rowlen < 4
		})
		peersrows[po] = strings.Join(row, " ")
		return true
	})

	for i := 0; i < k.MaxProxDisplay; i++ {
		if i == depth {
			rows = append(rows, fmt.Sprintf("============ DEPTH: %d ==========================================", i))
		}
		left := liverows[i]
		right := peersrows[i]
		if len(left) == 0 {
			left = " 0                             "
		}
		if len(right) == 0 {
			right = " 0"
		}
		rows = append(rows, fmt.Sprintf("%03d %v | %v", i, left, right))
	}
	rows = append(rows, "=========================================================================")
	return "\n" + strings.Join(rows, "\n")
}

type PeerPot struct {
	NNSet     [][]byte
	EmptyBins []int
}

func NewPeerPotMap(kadMinProxSize int, addrs [][]byte) map[string]*PeerPot {
	// create a table of all nodes for health check
	np := pot.NewPot(nil, 0)
	for _, addr := range addrs {
		np, _, _ = pot.Add(np, addr, pof)
	}
	ppmap := make(map[string]*PeerPot)

	for i, a := range addrs {
		pl := 256
		prev := 256
		var emptyBins []int
		var nns [][]byte
		np.EachNeighbour(addrs[i], pof, func(val pot.Val, po int) bool {
			a := val.([]byte)
			if po == 256 {
				return true
			}
			if pl == 256 || pl == po {
				nns = append(nns, a)
			}
			if pl == 256 && len(nns) >= kadMinProxSize {
				pl = po
				prev = po
			}
			if prev < pl {
				for j := prev; j > po; j-- {
					emptyBins = append(emptyBins, j)
				}
			}
			prev = po - 1
			return true
		})
		for j := prev; j >= 0; j-- {
			emptyBins = append(emptyBins, j)
		}
		log.Trace(fmt.Sprintf("%x NNS: %s", addrs[i][:4], LogAddrs(nns)))
		ppmap[common.Bytes2Hex(a)] = &PeerPot{nns, emptyBins}
	}
	return ppmap
}

func (k *Kademlia) saturation(n int) int {
	prev := -1
	k.addrs.EachBin(k.base, pof, 0, func(po, size int, f func(func(val pot.Val, i int) bool) bool) bool {
		prev++
		return prev == po && size >= n
	})
	depth := k.neighbourhoodDepth()
	if depth < prev {
		return depth
	}
	return prev
}

func (k *Kademlia) full(emptyBins []int) (full bool) {
	prev := 0
	e := len(emptyBins)
	ok := true
	depth := k.neighbourhoodDepth()
	k.conns.EachBin(k.base, pof, 0, func(po, _ int, _ func(func(val pot.Val, i int) bool) bool) bool {
		if prev == depth+1 {
			return true
		}
		for i := prev; i < po; i++ {
			e--
			if e < 0 {
				ok = false
				return false
			}
			if emptyBins[e] != i {
				log.Trace(fmt.Sprintf("%08x po: %d, i: %d, e: %d, emptybins: %v", k.BaseAddr()[:4], po, i, e, logEmptyBins(emptyBins)))
				if emptyBins[e] < i {
					panic("incorrect peerpot")
				}
				ok = false
				return false
			}
		}
		prev = po + 1
		return true
	})
	if !ok {
		return false
	}
	return e == 0
}

func (k *Kademlia) knowNearestNeighbours(peers [][]byte) bool {
	pm := make(map[string]bool)

	k.eachAddr(nil, 255, func(p OverlayAddr, po int, nn bool) bool {
		if !nn {
			return false
		}
		pk := fmt.Sprintf("%x", p.Address())
		pm[pk] = true
		return true
	})
	for _, p := range peers {
		pk := fmt.Sprintf("%x", p)
		if !pm[pk] {
			log.Trace(fmt.Sprintf("%08x: known nearest neighbour %s not found", k.BaseAddr()[:4], pk[:8]))
			return false
		}
	}
	return true
}

func (k *Kademlia) gotNearestNeighbours(peers [][]byte) (got bool, n int, missing [][]byte) {
	pm := make(map[string]bool)

	k.eachConn(nil, 255, func(p OverlayConn, po int, nn bool) bool {
		if !nn {
			return false
		}
		pk := fmt.Sprintf("%x", p.Address())
		pm[pk] = true
		return true
	})
	var gots int
	var culprits [][]byte
	for _, p := range peers {
		pk := fmt.Sprintf("%x", p)
		if pm[pk] {
			gots++
		} else {
			log.Trace(fmt.Sprintf("%08x: ExpNN: %s not found", k.BaseAddr()[:4], pk[:8]))
			culprits = append(culprits, p)
		}
	}
	return gots == len(peers), gots, culprits
}

type Health struct {
	KnowNN     bool     // whether node knows all its nearest neighbours
	GotNN      bool     // whether node is connected to all its nearest neighbours
	CountNN    int      // amount of nearest neighbors connected to
	CulpritsNN [][]byte // which known NNs are missing
	Full       bool     // whether node has a peer in each kademlia bin (where there is such a peer)
	Hive       string
}

func (k *Kademlia) Healthy(pp *PeerPot) *Health {
	k.lock.RLock()
	defer k.lock.RUnlock()
	gotnn, countnn, culpritsnn := k.gotNearestNeighbours(pp.NNSet)
	knownn := k.knowNearestNeighbours(pp.NNSet)
	full := k.full(pp.EmptyBins)
	log.Trace(fmt.Sprintf("%08x: healthy: knowNNs: %v, gotNNs: %v, full: %v\n", k.BaseAddr()[:4], knownn, gotnn, full))
	return &Health{knownn, gotnn, countnn, culpritsnn, full, k.string()}
}

func logEmptyBins(ebs []int) string {
	var ebss []string
	for _, eb := range ebs {
		ebss = append(ebss, fmt.Sprintf("%d", eb))
	}
	return strings.Join(ebss, ", ")
}
