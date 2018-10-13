package cnode

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"math/rand"
	"net"
	"strings"

	"github.com/5uwifi/canchain/lib/p2p/enr"
)

type Node struct {
	r  enr.Record
	id ID
}

func New(validSchemes enr.IdentityScheme, r *enr.Record) (*Node, error) {
	if err := r.VerifySignature(validSchemes); err != nil {
		return nil, err
	}
	node := &Node{r: *r}
	if n := copy(node.id[:], validSchemes.NodeAddr(&node.r)); n != len(ID{}) {
		return nil, fmt.Errorf("invalid node ID length %d, need %d", n, len(ID{}))
	}
	return node, nil
}

func (n *Node) ID() ID {
	return n.id
}

func (n *Node) Seq() uint64 {
	return n.r.Seq()
}

func (n *Node) Incomplete() bool {
	return n.IP() == nil
}

func (n *Node) Load(k enr.Entry) error {
	return n.r.Load(k)
}

func (n *Node) IP() net.IP {
	var ip net.IP
	n.Load((*enr.IP)(&ip))
	return ip
}

func (n *Node) UDP() int {
	var port enr.UDP
	n.Load(&port)
	return int(port)
}

func (n *Node) TCP() int {
	var port enr.TCP
	n.Load(&port)
	return int(port)
}

func (n *Node) Pubkey() *ecdsa.PublicKey {
	var key ecdsa.PublicKey
	if n.Load((*Secp256k1)(&key)) != nil {
		return nil
	}
	return &key
}

func (n *Node) Record() *enr.Record {
	cpy := n.r
	return &cpy
}

func (n *Node) ValidateComplete() error {
	if n.Incomplete() {
		return errors.New("incomplete node")
	}
	if n.UDP() == 0 {
		return errors.New("missing UDP port")
	}
	ip := n.IP()
	if ip.IsMulticast() || ip.IsUnspecified() {
		return errors.New("invalid IP (multicast/unspecified)")
	}
	var key Secp256k1
	return n.Load(&key)
}

func (n *Node) String() string {
	return n.v4URL()
}

func (n *Node) MarshalText() ([]byte, error) {
	return []byte(n.v4URL()), nil
}

func (n *Node) UnmarshalText(text []byte) error {
	dec, err := ParseV4(string(text))
	if err == nil {
		*n = *dec
	}
	return err
}

type ID [32]byte

func (n ID) Bytes() []byte {
	return n[:]
}

func (n ID) String() string {
	return fmt.Sprintf("%x", n[:])
}

func (n ID) GoString() string {
	return fmt.Sprintf("ccnode.HexID(\"%x\")", n[:])
}

func (n ID) TerminalString() string {
	return hex.EncodeToString(n[:8])
}

func (n ID) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(n[:])), nil
}

func (n *ID) UnmarshalText(text []byte) error {
	id, err := parseID(string(text))
	if err != nil {
		return err
	}
	*n = id
	return nil
}

func HexID(in string) ID {
	id, err := parseID(in)
	if err != nil {
		panic(err)
	}
	return id
}

func parseID(in string) (ID, error) {
	var id ID
	b, err := hex.DecodeString(strings.TrimPrefix(in, "0x"))
	if err != nil {
		return id, err
	} else if len(b) != len(id) {
		return id, fmt.Errorf("wrong length, want %d hex chars", len(id)*2)
	}
	copy(id[:], b)
	return id, nil
}

func DistCmp(target, a, b ID) int {
	for i := range target {
		da := a[i] ^ target[i]
		db := b[i] ^ target[i]
		if da > db {
			return 1
		} else if da < db {
			return -1
		}
	}
	return 0
}

func LogDist(a, b ID) int {
	lz := 0
	for i := range a {
		x := a[i] ^ b[i]
		if x == 0 {
			lz += 8
		} else {
			lz += bits.LeadingZeros8(x)
			break
		}
	}
	return len(a)*8 - lz
}

func RandomID(a ID, n int) (b ID) {
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
