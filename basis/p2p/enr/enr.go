
//
//
//
package enr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/5uwifi/canchain/basis/rlp"
)

const SizeLimit = 300 // maximum encoded size of a node record in bytes

var (
	errNoID           = errors.New("unknown or unspecified identity scheme")
	errInvalidSig     = errors.New("invalid signature")
	errNotSorted      = errors.New("record key/value pairs are not sorted by key")
	errDuplicateKey   = errors.New("record contains duplicate key")
	errIncompletePair = errors.New("record contains incomplete k/v pair")
	errTooBig         = fmt.Errorf("record bigger than %d bytes", SizeLimit)
	errEncodeUnsigned = errors.New("can't encode unsigned record")
	errNotFound       = errors.New("no such key in record")
)

type Record struct {
	seq       uint64 // sequence number
	signature []byte // the signature
	raw       []byte // RLP encoded record
	pairs     []pair // sorted list of all key/value pairs
}

type pair struct {
	k string
	v rlp.RawValue
}

func (r *Record) Signed() bool {
	return r.signature != nil
}

func (r *Record) Seq() uint64 {
	return r.seq
}

func (r *Record) SetSeq(s uint64) {
	r.signature = nil
	r.raw = nil
	r.seq = s
}

//
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
		// element is present at r.pairs[i]
		pairs[i].v = blob
	case i < len(r.pairs):
		// insert pair before i-th elem
		el := pair{e.ENRKey(), blob}
		pairs = append(pairs, pair{})
		copy(pairs[i+1:], pairs[i:])
		pairs[i] = el
	default:
		// element should be placed at the end of r.pairs
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
	if !r.Signed() {
		return errEncodeUnsigned
	}
	_, err := w.Write(r.raw)
	return err
}

func (r *Record) DecodeRLP(s *rlp.Stream) error {
	raw, err := s.Raw()
	if err != nil {
		return err
	}
	if len(raw) > SizeLimit {
		return errTooBig
	}

	// Decode the RLP container.
	dec := Record{raw: raw}
	s = rlp.NewStream(bytes.NewReader(raw), 0)
	if _, err := s.List(); err != nil {
		return err
	}
	if err = s.Decode(&dec.signature); err != nil {
		return err
	}
	if err = s.Decode(&dec.seq); err != nil {
		return err
	}
	// The rest of the record contains sorted k/v pairs.
	var prevkey string
	for i := 0; ; i++ {
		var kv pair
		if err := s.Decode(&kv.k); err != nil {
			if err == rlp.EOL {
				break
			}
			return err
		}
		if err := s.Decode(&kv.v); err != nil {
			if err == rlp.EOL {
				return errIncompletePair
			}
			return err
		}
		if i > 0 {
			if kv.k == prevkey {
				return errDuplicateKey
			}
			if kv.k < prevkey {
				return errNotSorted
			}
		}
		dec.pairs = append(dec.pairs, kv)
		prevkey = kv.k
	}
	if err := s.ListEnd(); err != nil {
		return err
	}

	_, scheme := dec.idScheme()
	if scheme == nil {
		return errNoID
	}
	if err := scheme.Verify(&dec, dec.signature); err != nil {
		return err
	}
	*r = dec
	return nil
}

func (r *Record) NodeAddr() []byte {
	_, scheme := r.idScheme()
	if scheme == nil {
		return nil
	}
	return scheme.NodeAddr(r)
}

func (r *Record) SetSig(idscheme string, sig []byte) error {
	// Check that "id" is set and matches the given scheme. This panics because
	// inconsitencies here are always implementation bugs in the signing function calling
	// this method.
	id, s := r.idScheme()
	if s == nil {
		panic(errNoID)
	}
	if id != idscheme {
		panic(fmt.Errorf("identity scheme mismatch in Sign: record has %s, want %s", id, idscheme))
	}

	// Verify against the scheme.
	if err := s.Verify(r, sig); err != nil {
		return err
	}
	raw, err := r.encode(sig)
	if err != nil {
		return err
	}
	r.signature, r.raw = sig, raw
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

func (r *Record) idScheme() (string, IdentityScheme) {
	var id ID
	if err := r.Load(&id); err != nil {
		return "", nil
	}
	return string(id), FindIdentityScheme(string(id))
}
