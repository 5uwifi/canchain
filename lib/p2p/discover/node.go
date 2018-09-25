package discover

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"net"
	"time"

	"github.com/5uwifi/canchain/common/math"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/crypto/secp256k1"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
)

type node struct {
	cnode.Node
	addedAt time.Time
}

type encPubkey [64]byte

func encodePubkey(key *ecdsa.PublicKey) encPubkey {
	var e encPubkey
	math.ReadBits(key.X, e[:len(e)/2])
	math.ReadBits(key.Y, e[len(e)/2:])
	return e
}

func decodePubkey(e encPubkey) (*ecdsa.PublicKey, error) {
	p := &ecdsa.PublicKey{Curve: crypto.S256(), X: new(big.Int), Y: new(big.Int)}
	half := len(e) / 2
	p.X.SetBytes(e[:half])
	p.Y.SetBytes(e[half:])
	if !p.Curve.IsOnCurve(p.X, p.Y) {
		return nil, errors.New("invalid secp256k1 curve point")
	}
	return p, nil
}

func (e encPubkey) id() cnode.ID {
	return cnode.ID(crypto.Keccak256Hash(e[:]))
}

func recoverNodeKey(hash, sig []byte) (key encPubkey, err error) {
	pubkey, err := secp256k1.RecoverPubkey(hash, sig)
	if err != nil {
		return key, err
	}
	copy(key[:], pubkey[1:])
	return key, nil
}

func wrapNode(n *cnode.Node) *node {
	return &node{Node: *n}
}

func wrapNodes(ns []*cnode.Node) []*node {
	result := make([]*node, len(ns))
	for i, n := range ns {
		result[i] = wrapNode(n)
	}
	return result
}

func unwrapNode(n *node) *cnode.Node {
	return &n.Node
}

func unwrapNodes(ns []*node) []*cnode.Node {
	result := make([]*cnode.Node, len(ns))
	for i, n := range ns {
		result[i] = unwrapNode(n)
	}
	return result
}

func (n *node) addr() *net.UDPAddr {
	return &net.UDPAddr{IP: n.IP(), Port: n.UDP()}
}

func (n *node) String() string {
	return n.Node.String()
}
