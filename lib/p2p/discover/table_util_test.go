package discover

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net"
	"sync"

	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/enr"
)

func newTestTable(t transport) (*Table, *cnode.DB) {
	var r enr.Record
	r.Set(enr.IP{0, 0, 0, 0})
	n := cnode.SignNull(&r, cnode.ID{})
	db, _ := cnode.OpenDB("")
	tab, _ := newTable(t, n, db, nil)
	return tab, db
}

func nodeAtDistance(base cnode.ID, ld int, ip net.IP) *node {
	var r enr.Record
	r.Set(enr.IP(ip))
	return wrapNode(cnode.SignNull(&r, idAtDistance(base, ld)))
}

func idAtDistance(a cnode.ID, n int) (b cnode.ID) {
	if n == 0 {
		return a
	}
	b = a
	pos := len(a) - n/8 - 1
	bit := byte(0x01) << (byte(n%8) - 1)
	if bit == 0 {
		pos++
		bit = 0x80
	}
	b[pos] = a[pos]&^bit | ^a[pos]&bit
	for i := pos + 1; i < len(a); i++ {
		b[i] = byte(rand.Intn(255))
	}
	return b
}

func intIP(i int) net.IP {
	return net.IP{byte(i), 0, 2, byte(i)}
}

func fillBucket(tab *Table, n *node) (last *node) {
	ld := cnode.LogDist(tab.self.ID(), n.ID())
	b := tab.bucket(n.ID())
	for len(b.entries) < bucketSize {
		b.entries = append(b.entries, nodeAtDistance(tab.self.ID(), ld, intIP(ld)))
	}
	return b.entries[bucketSize-1]
}

type pingRecorder struct {
	mu           sync.Mutex
	dead, pinged map[cnode.ID]bool
}

func newPingRecorder() *pingRecorder {
	return &pingRecorder{
		dead:   make(map[cnode.ID]bool),
		pinged: make(map[cnode.ID]bool),
	}
}

func (t *pingRecorder) findnode(toid cnode.ID, toaddr *net.UDPAddr, target encPubkey) ([]*node, error) {
	return nil, nil
}

func (t *pingRecorder) waitping(from cnode.ID) error {
	return nil
}

func (t *pingRecorder) ping(toid cnode.ID, toaddr *net.UDPAddr) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.pinged[toid] = true
	if t.dead[toid] {
		return errTimeout
	} else {
		return nil
	}
}

func (t *pingRecorder) close() {}

func hasDuplicates(slice []*node) bool {
	seen := make(map[cnode.ID]bool)
	for i, e := range slice {
		if e == nil {
			panic(fmt.Sprintf("nil *Node at %d", i))
		}
		if seen[e.ID()] {
			return true
		}
		seen[e.ID()] = true
	}
	return false
}

func contains(ns []*node, id cnode.ID) bool {
	for _, n := range ns {
		if n.ID() == id {
			return true
		}
	}
	return false
}

func sortedByDistanceTo(distbase cnode.ID, slice []*node) bool {
	var last cnode.ID
	for i, e := range slice {
		if i > 0 && cnode.DistCmp(distbase, e.ID(), last) < 0 {
			return false
		}
		last = e.ID()
	}
	return true
}

func hexEncPubkey(h string) (ret encPubkey) {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	if len(b) != len(ret) {
		panic("invalid length")
	}
	copy(ret[:], b)
	return ret
}

func hexPubkey(h string) *ecdsa.PublicKey {
	k, err := decodePubkey(hexEncPubkey(h))
	if err != nil {
		panic(err)
	}
	return k
}
