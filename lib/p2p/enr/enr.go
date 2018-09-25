package enr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/5uwifi/canchain/lib/rlp"
)

const SizeLimit = 300

var (
	ErrInvalidSig     = errors.New("invalid signature on node record")
	errNotSorted      = errors.New("record key/value pairs are not sorted by key")
	errDuplicateKey   = errors.New("record contains duplicate key")
	errIncompletePair = errors.New("record contains incomplete k/v pair")
	errTooBig         = fmt.Errorf("record bigger than %d bytes", SizeLimit)
	errEncodeUnsigned = errors.New("can't encode unsigned record")
	errNotFound       = errors.New("no such key in record")
)

type IdentityScheme interface {
	Verify(r *Record, sig []byte) error
	NodeAddr(r *Record) []byte
}

type SchemeMap map[string]IdentityScheme

func (m SchemeMap) Verify(r *Record, sig []byte) error {
	s := m[r.IdentityScheme()]
	if s == nil {
		return ErrInvalidSig
	}
	return s.Verify(r, sig)
}

func (m SchemeMap) NodeAddr(r *Record) []byte {
	s := m[r.IdentityScheme()]
	if s == nil {
		return nil
	}
	return s.NodeAddr(r)
}

type Record struct {
	seq       uint64
	signature []byte
	raw       []byte
	pairs     []pair
}

type pair struct {
	k string
	v rlp.RawValue
}

func (r *Record) Seq() uint64 {
	return r.seq
}

func (r *Record) SetSeq(s uint64) {
	r.signature = nil
	r.raw = nil
	r.seq = s
}

func (r *Record) Load(e Entry) error {
	i := sort.Search(len(r.pairs), func(i int) bool { return r.pairs[i].k >= e.ENRKey() })
	if i < len(r.pairs) && r.pairs[i].k == e.ENRKey() {
		if err := rlp.DecodeBytes(r.pairs[i].v, e); err != nil {
			return &KeyError{Key: e.ENRKey(), Err: err}
		}
		return nil
	}
	return &KeyError{Key: e.ENRKey(), Err: errNotFound}
}

func (r *Record) Set(e Entry) {
	blob, err := rlp.EncodeToBytes(e)
	if err != nil {
		panic(fmt.Errorf("enr: can't encode %s: %v", e.ENRKey(), err))
	}
	r.invalidate()

	pairs := make([]pair, len(r.pairs))
	copy(pairs, r.pairs)
	i := sort.Search(len(pairs), func(i int) bool { return pairs[i].k >= e.ENRKey() })
	switch {
	case i < len(pairs) && pairs[i].k == e.ENRKey():
		pairs[i].v = blob
	case i < len(r.pairs):
		el := pair{e.ENRKey(), blob}
		pairs = append(pairs, pair{})
		copy(pairs[i+1:], pairs[i:])
		pairs[i] = el
	default:
		pairs = append(pairs, pair{e.ENRKey(), blob})
	}
	r.pairs = pairs
}

func (r *Record) invalidate() {
	if r.signature == nil {
		r.seq++
	}
	r.signature = nil
	r.raw = nil
}

func (r Record) EncodeRLP(w io.Writer) error {
	if r.signature == nil {
		return errEncodeUnsigned
	}
	_, err := w.Write(r.raw)
	return err
}

func (r *Record) DecodeRLP(s *rlp.Stream) error {
	dec, raw, err := decodeRecord(s)
	if err != nil {
		return err
	}
	*r = dec
	r.raw = raw
	return nil
}

func decodeRecord(s *rlp.Stream) (dec Record, raw []byte, err error) {
	raw, err = s.Raw()
	if err != nil {
		return dec, raw, err
	}
	if len(raw) > SizeLimit {
		return dec, raw, errTooBig
	}

	s = rlp.NewStream(bytes.NewReader(raw), 0)
	if _, err := s.List(); err != nil {
		return dec, raw, err
	}
	if err = s.Decode(&dec.signature); err != nil {
		return dec, raw, err
	}
	if err = s.Decode(&dec.seq); err != nil {
		return dec, raw, err
	}
	var prevkey string
	for i := 0; ; i++ {
		var kv pair
		if err := s.Decode(&kv.k); err != nil {
			if err == rlp.EOL {
				break
			}
			return dec, raw, err
		}
		if err := s.Decode(&kv.v); err != nil {
			if err == rlp.EOL {
				return dec, raw, errIncompletePair
			}
			return dec, raw, err
		}
		if i > 0 {
			if kv.k == prevkey {
				return dec, raw, errDuplicateKey
			}
			if kv.k < prevkey {
				return dec, raw, errNotSorted
			}
		}
		dec.pairs = append(dec.pairs, kv)
		prevkey = kv.k
	}
	return dec, raw, s.ListEnd()
}

func (r *Record) IdentityScheme() string {
	var id ID
	r.Load(&id)
	return string(id)
}

func (r *Record) VerifySignature(s IdentityScheme) error {
	return s.Verify(r, r.signature)
}

func (r *Record) SetSig(s IdentityScheme, sig []byte) error {
	switch {
	case s == nil && sig != nil:
		panic("enr: invalid call to SetSig with non-nil signature but nil scheme")
	case s != nil && sig == nil:
		panic("enr: invalid call to SetSig with nil signature but non-nil scheme")
	case s != nil:
		if err := s.Verify(r, sig); err != nil {
			return err
		}
		raw, err := r.encode(sig)
		if err != nil {
			return err
		}
		r.signature, r.raw = sig, raw
	default:
		r.signature, r.raw = nil, nil
	}
	return nil
}

func (r *Record) AppendElements(list []interface{}) []interface{} {
	list = append(list, r.seq)
	for _, p := range r.pairs {
		list = append(list, p.k, p.v)
	}
	return list
}

func (r *Record) encode(sig []byte) (raw []byte, err error) {
	list := make([]interface{}, 1, 2*len(r.pairs)+1)
	list[0] = sig
	list = r.AppendElements(list)
	if raw, err = rlp.EncodeToBytes(list); err != nil {
		return nil, err
	}
	if len(raw) > SizeLimit {
		return nil, errTooBig
	}
	return raw, nil
}
