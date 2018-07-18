
package bmt

import (
	"fmt"
	"hash"
	"strings"
	"sync"
	"sync/atomic"
)

/*
Binary Merkle Tree Hash is a hash function over arbitrary datachunks of limited size
It is defined as the root hash of the binary merkle tree built over fixed size segments
of the underlying chunk using any base hash function (e.g keccak 256 SHA3).
Chunk with data shorter than the fixed size are hashed as if they had zero padding

BMT hash is used as the chunk hash function in swarm which in turn is the basis for the
128 branching swarm hash http://swarm-guide.readthedocs.io/en/latest/architecture.html#swarm-hash

The BMT is optimal for providing compact inclusion proofs, i.e. prove that a
segment is a substring of a chunk starting at a particular offset
The size of the underlying segments is fixed to the size of the base hash (called the resolution
of the BMT hash), Using Keccak256 SHA3 hash is 32 bytes, the EVM word size to optimize for on-chain BMT verification
as well as the hash size optimal for inclusion proofs in the merkle tree of the swarm hash.

Two implementations are provided:

* RefHasher is optimized for code simplicity and meant as a reference implementation
  that is simple to understand
* Hasher is optimized for speed taking advantage of concurrency with minimalistic
  control structure to coordinate the concurrent routines
  It implements the following interfaces
	* standard golang hash.Hash
	* SwarmHash
	* io.Writer
	* TODO: SegmentWriter
*/

const (
	// SegmentCount is the maximum number of segments of the underlying chunk
	// Should be equal to max-chunk-data-size / hash-size
	SegmentCount = 128
	// PoolSize is the maximum number of bmt trees used by the hashers, i.e,
	// the maximum number of concurrent BMT hashing operations performed by the same hasher
	PoolSize = 8
)

type BaseHasherFunc func() hash.Hash

// - implements the hash.Hash interface
// - reuses a pool of trees for amortised memory allocation and resource control
// - supports order-agnostic concurrent segment writes (TODO:)
// - the same hasher instance must not be called concurrently on more than one chunk
// - the same hasher instance is synchronously reuseable
// - Sum gives back the tree to the pool and guaranteed to leave
// - generates and verifies segment inclusion proofs (TODO:)
type Hasher struct {
	pool *TreePool // BMT resource pool
	bmt  *tree     // prebuilt BMT resource for flowcontrol and proofs
}

func New(p *TreePool) *Hasher {
	return &Hasher{
		pool: p,
	}
}

type TreePool struct {
	lock         sync.Mutex
	c            chan *tree     // the channel to obtain a resource from the pool
	hasher       BaseHasherFunc // base hasher to use for the BMT levels
	SegmentSize  int            // size of leaf segments, stipulated to be = hash size
	SegmentCount int            // the number of segments on the base level of the BMT
	Capacity     int            // pool capacity, controls concurrency
	Depth        int            // depth of the bmt trees = int(log2(segmentCount))+1
	Datalength   int            // the total length of the data (count * size)
	count        int            // current count of (ever) allocated resources
	zerohashes   [][]byte       // lookup table for predictable padding subtrees for all levels
}

func NewTreePool(hasher BaseHasherFunc, segmentCount, capacity int) *TreePool {
	// initialises the zerohashes lookup table
	depth := calculateDepthFor(segmentCount)
	segmentSize := hasher().Size()
	zerohashes := make([][]byte, depth)
	zeros := make([]byte, segmentSize)
	zerohashes[0] = zeros
	h := hasher()
	for i := 1; i < depth; i++ {
		h.Reset()
		h.Write(zeros)
		h.Write(zeros)
		zeros = h.Sum(nil)
		zerohashes[i] = zeros
	}
	return &TreePool{
		c:            make(chan *tree, capacity),
		hasher:       hasher,
		SegmentSize:  segmentSize,
		SegmentCount: segmentCount,
		Capacity:     capacity,
		Datalength:   segmentCount * segmentSize,
		Depth:        depth,
		zerohashes:   zerohashes,
	}
}

func (p *TreePool) Drain(n int) {
	p.lock.Lock()
	defer p.lock.Unlock()
	for len(p.c) > n {
		<-p.c
		p.count--
	}
}

func (p *TreePool) reserve() *tree {
	p.lock.Lock()
	defer p.lock.Unlock()
	var t *tree
	if p.count == p.Capacity {
		return <-p.c
	}
	select {
	case t = <-p.c:
	default:
		t = newTree(p.SegmentSize, p.Depth)
		p.count++
	}
	return t
}

func (p *TreePool) release(t *tree) {
	p.c <- t // can never fail ...
}

type tree struct {
	leaves  []*node     // leaf nodes of the tree, other nodes accessible via parent links
	cur     int         // index of rightmost currently open segment
	offset  int         // offset (cursor position) within currently open segment
	segment []byte      // the rightmost open segment (not complete)
	section []byte      // the rightmost open section (double segment)
	depth   int         // number of levels
	result  chan []byte // result channel
	hash    []byte      // to record the result
	span    []byte      // The span of the data subsumed under the chunk
}

type node struct {
	isLeft      bool   // whether it is left side of the parent double segment
	parent      *node  // pointer to parent node in the BMT
	state       int32  // atomic increment impl concurrent boolean toggle
	left, right []byte // this is where the content segment is set
}

func newNode(index int, parent *node) *node {
	return &node{
		parent: parent,
		isLeft: index%2 == 0,
	}
}

func (t *tree) draw(hash []byte) string {
	var left, right []string
	var anc []*node
	for i, n := range t.leaves {
		left = append(left, fmt.Sprintf("%v", hashstr(n.left)))
		if i%2 == 0 {
			anc = append(anc, n.parent)
		}
		right = append(right, fmt.Sprintf("%v", hashstr(n.right)))
	}
	anc = t.leaves
	var hashes [][]string
	for l := 0; len(anc) > 0; l++ {
		var nodes []*node
		hash := []string{""}
		for i, n := range anc {
			hash = append(hash, fmt.Sprintf("%v|%v", hashstr(n.left), hashstr(n.right)))
			if i%2 == 0 && n.parent != nil {
				nodes = append(nodes, n.parent)
			}
		}
		hash = append(hash, "")
		hashes = append(hashes, hash)
		anc = nodes
	}
	hashes = append(hashes, []string{"", fmt.Sprintf("%v", hashstr(hash)), ""})
	total := 60
	del := "                             "
	var rows []string
	for i := len(hashes) - 1; i >= 0; i-- {
		var textlen int
		hash := hashes[i]
		for _, s := range hash {
			textlen += len(s)
		}
		if total < textlen {
			total = textlen + len(hash)
		}
		delsize := (total - textlen) / (len(hash) - 1)
		if delsize > len(del) {
			delsize = len(del)
		}
		row := fmt.Sprintf("%v: %v", len(hashes)-i-1, strings.Join(hash, del[:delsize]))
		rows = append(rows, row)

	}
	rows = append(rows, strings.Join(left, "  "))
	rows = append(rows, strings.Join(right, "  "))
	return strings.Join(rows, "\n") + "\n"
}

// - segment size is stipulated to be the size of the hash
func newTree(segmentSize, depth int) *tree {
	n := newNode(0, nil)
	prevlevel := []*node{n}
	// iterate over levels and creates 2^(depth-level) nodes
	count := 2
	for level := depth - 2; level >= 0; level-- {
		nodes := make([]*node, count)
		for i := 0; i < count; i++ {
			parent := prevlevel[i/2]
			nodes[i] = newNode(i, parent)
		}
		prevlevel = nodes
		count *= 2
	}
	// the datanode level is the nodes on the last level
	return &tree{
		leaves:  prevlevel,
		result:  make(chan []byte, 1),
		segment: make([]byte, segmentSize),
		section: make([]byte, 2*segmentSize),
	}
}


func (h *Hasher) Size() int {
	return h.pool.SegmentSize
}

func (h *Hasher) BlockSize() int {
	return h.pool.SegmentSize
}

func Hash(h *Hasher, span, data []byte) []byte {
	h.ResetWithLength(span)
	h.Write(data)
	return h.Sum(nil)
}

func (h *Hasher) DataLength() int {
	return h.pool.Datalength
}

func (h *Hasher) Sum(b []byte) (r []byte) {
	return h.sum(b, true, true)
}

// * if the tree is released right away
// * if sequential write is used (can read sections)
func (h *Hasher) sum(b []byte, release, section bool) (r []byte) {
	t := h.bmt
	h.finalise(section)
	if t.offset > 0 { // get the last node (double segment)

		// padding the segment  with zero
		copy(t.segment[t.offset:], h.pool.zerohashes[0])
	}
	if section {
		if t.cur%2 == 1 {
			// if just finished current segment, copy it to the right half of the chunk
			copy(t.section[h.pool.SegmentSize:], t.segment)
		} else {
			// copy segment to front of section, zero pad the right half
			copy(t.section, t.segment)
			copy(t.section[h.pool.SegmentSize:], h.pool.zerohashes[0])
		}
		h.writeSection(t.cur, t.section)
	} else {
		// TODO: h.writeSegment(t.cur, t.segment)
		panic("SegmentWriter not implemented")
	}
	bmtHash := <-t.result
	span := t.span

	if release {
		h.releaseTree()
	}
	// sha3(span + BMT(pure_chunk))
	if span == nil {
		return bmtHash
	}
	bh := h.pool.hasher()
	bh.Reset()
	bh.Write(span)
	bh.Write(bmtHash)
	return bh.Sum(b)
}



func (h *Hasher) Write(b []byte) (int, error) {
	l := len(b)
	if l <= 0 {
		return 0, nil
	}
	t := h.bmt
	need := (h.pool.SegmentCount - t.cur) * h.pool.SegmentSize
	if l < need {
		need = l
	}
	// calculate missing bit to complete current open segment
	rest := h.pool.SegmentSize - t.offset
	if need < rest {
		rest = need
	}
	copy(t.segment[t.offset:], b[:rest])
	need -= rest
	size := (t.offset + rest) % h.pool.SegmentSize
	// read full segments and the last possibly partial segment
	for need > 0 {
		// push all finished chunks we read
		if t.cur%2 == 0 {
			copy(t.section, t.segment)
		} else {
			copy(t.section[h.pool.SegmentSize:], t.segment)
			h.writeSection(t.cur, t.section)
		}
		size = h.pool.SegmentSize
		if need < size {
			size = need
		}
		copy(t.segment, b[rest:rest+size])
		need -= size
		rest += size
		t.cur++
	}
	t.offset = size % h.pool.SegmentSize
	return l, nil
}

func (h *Hasher) Reset() {
	h.getTree()
}


func (h *Hasher) ResetWithLength(span []byte) {
	h.Reset()
	h.bmt.span = span
}

func (h *Hasher) releaseTree() {
	t := h.bmt
	if t != nil {
		t.cur = 0
		t.offset = 0
		t.span = nil
		t.hash = nil
		h.bmt = nil
		h.pool.release(t)
	}
}

// 	go h.run(h.bmt.leaves[i/2], h.pool.hasher(), i%2 == 0, s)
// }

func (h *Hasher) writeSection(i int, section []byte) {
	n := h.bmt.leaves[i/2]
	isLeft := n.isLeft
	n = n.parent
	bh := h.pool.hasher()
	bh.Write(section)
	go func() {
		sum := bh.Sum(nil)
		if n == nil {
			h.bmt.result <- sum
			return
		}
		h.run(n, bh, isLeft, sum)
	}()
}

func (h *Hasher) run(n *node, bh hash.Hash, isLeft bool, s []byte) {
	for {
		if isLeft {
			n.left = s
		} else {
			n.right = s
		}
		// the child-thread first arriving will quit
		if n.toggle() {
			return
		}
		// the second thread now can be sure both left and right children are written
		// it calculates the hash of left|right and take it to the next level
		bh.Reset()
		bh.Write(n.left)
		bh.Write(n.right)
		s = bh.Sum(nil)

		// at the root of the bmt just write the result to the result channel
		if n.parent == nil {
			h.bmt.result <- s
			return
		}

		// otherwise iterate on parent
		isLeft = n.isLeft
		n = n.parent
	}
}

func (h *Hasher) finalise(skip bool) {
	t := h.bmt
	isLeft := t.cur%2 == 0
	n := t.leaves[t.cur/2]
	for level := 0; n != nil; level++ {
		// when the final segment's path is going via left child node
		// we include an all-zero subtree hash for the right level and toggle the node.
		// when the path is going through right child node, nothing to do
		if isLeft && !skip {
			n.right = h.pool.zerohashes[level]
			n.toggle()
		}
		skip = false
		isLeft = n.isLeft
		n = n.parent
	}
}

func (h *Hasher) getTree() *tree {
	if h.bmt != nil {
		return h.bmt
	}
	t := h.pool.reserve()
	h.bmt = t
	return t
}

func (n *node) toggle() bool {
	return atomic.AddInt32(&n.state, 1)%2 == 1
}

func hashstr(b []byte) string {
	end := len(b)
	if end > 4 {
		end = 4
	}
	return fmt.Sprintf("%x", b[:end])
}

func calculateDepthFor(n int) (d int) {
	c := 2
	for ; c < n; c *= 2 {
		d++
	}
	return d + 1
}
