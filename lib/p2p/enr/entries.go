package enr

import (
	"fmt"
	"io"
	"net"

	"github.com/5uwifi/canchain/lib/rlp"
)

type Entry interface {
	ENRKey() string
}

type generic struct {
	key   string
	value interface{}
}

func (g generic) ENRKey() string { return g.key }

func (g generic) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, g.value)
}

func (g *generic) DecodeRLP(s *rlp.Stream) error {
	return s.Decode(g.value)
}

func WithEntry(k string, v interface{}) Entry {
	return &generic{key: k, value: v}
}

type TCP uint16

func (v TCP) ENRKey() string { return "tcp" }

type UDP uint16

func (v UDP) ENRKey() string { return "udp" }

type ID string

const IDv4 = ID("v4")

func (v ID) ENRKey() string { return "id" }

type IP net.IP

func (v IP) ENRKey() string { return "ip" }

func (v IP) EncodeRLP(w io.Writer) error {
	if ip4 := net.IP(v).To4(); ip4 != nil {
		return rlp.Encode(w, ip4)
	}
	return rlp.Encode(w, net.IP(v))
}

func (v *IP) DecodeRLP(s *rlp.Stream) error {
	if err := s.Decode((*net.IP)(v)); err != nil {
		return err
	}
	if len(*v) != 4 && len(*v) != 16 {
		return fmt.Errorf("invalid IP address, want 4 or 16 bytes: %v", *v)
	}
	return nil
}

type KeyError struct {
	Key string
	Err error
}

func (err *KeyError) Error() string {
	if err.Err == errNotFound {
		return fmt.Sprintf("missing ENR key %q", err.Key)
	}
	return fmt.Sprintf("ENR key %q: %v", err.Key, err.Err)
}

func IsNotFound(err error) bool {
	kerr, ok := err.(*KeyError)
	return ok && kerr.Err == errNotFound
}
