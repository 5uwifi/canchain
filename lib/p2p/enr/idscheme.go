package enr

import (
	"crypto/ecdsa"
	"fmt"
	"sync"

	"github.com/5uwifi/canchain/common/math"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/crypto/sha3"
	"github.com/5uwifi/canchain/lib/rlp"
)

var schemes sync.Map

type IdentityScheme interface {
	Verify(r *Record, sig []byte) error
	NodeAddr(r *Record) []byte
}

func RegisterIdentityScheme(name string, scheme IdentityScheme) {
	if _, loaded := schemes.LoadOrStore(name, scheme); loaded {
		panic("identity scheme " + name + " already registered")
	}
}

func FindIdentityScheme(name string) IdentityScheme {
	s, ok := schemes.Load(name)
	if !ok {
		return nil
	}
	return s.(IdentityScheme)
}

type v4ID struct{}

func init() {
	RegisterIdentityScheme("v4", v4ID{})
}

func SignV4(r *Record, privkey *ecdsa.PrivateKey) error {
	cpy := *r
	cpy.Set(ID("v4"))
	cpy.Set(Secp256k1(privkey.PublicKey))

	h := sha3.NewKeccak256()
	rlp.Encode(h, cpy.AppendElements(nil))
	sig, err := crypto.Sign(h.Sum(nil), privkey)
	if err != nil {
		return err
	}
	sig = sig[:len(sig)-1]
	if err = cpy.SetSig("v4", sig); err == nil {
		*r = cpy
	}
	return err
}

type s256raw []byte

func (s256raw) ENRKey() string { return "secp256k1" }

func (v4ID) Verify(r *Record, sig []byte) error {
	var entry s256raw
	if err := r.Load(&entry); err != nil {
		return err
	} else if len(entry) != 33 {
		return fmt.Errorf("invalid public key")
	}

	h := sha3.NewKeccak256()
	rlp.Encode(h, r.AppendElements(nil))
	if !crypto.VerifySignature(entry, h.Sum(nil), sig) {
		return errInvalidSig
	}
	return nil
}

func (v4ID) NodeAddr(r *Record) []byte {
	var pubkey Secp256k1
	err := r.Load(&pubkey)
	if err != nil {
		return nil
	}
	buf := make([]byte, 64)
	math.ReadBits(pubkey.X, buf[:32])
	math.ReadBits(pubkey.Y, buf[32:])
	return crypto.Keccak256(buf)
}
