

package whisperv5

import (
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"
	gmath "math"
	"math/big"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/math"
	"github.com/5uwifi/canchain/basis/crypto"
	"github.com/5uwifi/canchain/basis/crypto/ecies"
	"github.com/5uwifi/canchain/basis/rlp"
)

type Envelope struct {
	Version  []byte
	Expiry   uint32
	TTL      uint32
	Topic    TopicType
	AESNonce []byte
	Data     []byte
	EnvNonce uint64

	pow  float64     // Message-specific PoW as described in the Whisper specification.
	hash common.Hash // Cached hash of the envelope to avoid rehashing every time.
	// Don't access hash directly, use Hash() function instead.
}

func (e *Envelope) size() int {
	return 20 + len(e.Version) + len(e.AESNonce) + len(e.Data)
}

func (e *Envelope) rlpWithoutNonce() []byte {
	res, _ := rlp.EncodeToBytes([]interface{}{e.Version, e.Expiry, e.TTL, e.Topic, e.AESNonce, e.Data})
	return res
}

func NewEnvelope(ttl uint32, topic TopicType, aesNonce []byte, msg *sentMessage) *Envelope {
	env := Envelope{
		Version:  make([]byte, 1),
		Expiry:   uint32(time.Now().Add(time.Second * time.Duration(ttl)).Unix()),
		TTL:      ttl,
		Topic:    topic,
		AESNonce: aesNonce,
		Data:     msg.Raw,
		EnvNonce: 0,
	}

	if EnvelopeVersion < 256 {
		env.Version[0] = byte(EnvelopeVersion)
	} else {
		panic("please increase the size of Envelope.Version before releasing this version")
	}

	return &env
}

func (e *Envelope) IsSymmetric() bool {
	return len(e.AESNonce) > 0
}

func (e *Envelope) isAsymmetric() bool {
	return !e.IsSymmetric()
}

func (e *Envelope) Ver() uint64 {
	return bytesToUintLittleEndian(e.Version)
}

func (e *Envelope) Seal(options *MessageParams) error {
	var target, bestBit int
	if options.PoW == 0 {
		// adjust for the duration of Seal() execution only if execution time is predefined unconditionally
		e.Expiry += options.WorkTime
	} else {
		target = e.powToFirstBit(options.PoW)
		if target < 1 {
			target = 1
		}
	}

	buf := make([]byte, 64)
	h := crypto.Keccak256(e.rlpWithoutNonce())
	copy(buf[:32], h)

	finish := time.Now().Add(time.Duration(options.WorkTime) * time.Second).UnixNano()
	for nonce := uint64(0); time.Now().UnixNano() < finish; {
		for i := 0; i < 1024; i++ {
			binary.BigEndian.PutUint64(buf[56:], nonce)
			d := new(big.Int).SetBytes(crypto.Keccak256(buf))
			firstBit := math.FirstBitSet(d)
			if firstBit > bestBit {
				e.EnvNonce, bestBit = nonce, firstBit
				if target > 0 && bestBit >= target {
					return nil
				}
			}
			nonce++
		}
	}

	if target > 0 && bestBit < target {
		return fmt.Errorf("failed to reach the PoW target, specified pow time (%d seconds) was insufficient", options.WorkTime)
	}

	return nil
}

func (e *Envelope) PoW() float64 {
	if e.pow == 0 {
		e.calculatePoW(0)
	}
	return e.pow
}

func (e *Envelope) calculatePoW(diff uint32) {
	buf := make([]byte, 64)
	h := crypto.Keccak256(e.rlpWithoutNonce())
	copy(buf[:32], h)
	binary.BigEndian.PutUint64(buf[56:], e.EnvNonce)
	d := new(big.Int).SetBytes(crypto.Keccak256(buf))
	firstBit := math.FirstBitSet(d)
	x := gmath.Pow(2, float64(firstBit))
	x /= float64(e.size())
	x /= float64(e.TTL + diff)
	e.pow = x
}

func (e *Envelope) powToFirstBit(pow float64) int {
	x := pow
	x *= float64(e.size())
	x *= float64(e.TTL)
	bits := gmath.Log2(x)
	bits = gmath.Ceil(bits)
	return int(bits)
}

func (e *Envelope) Hash() common.Hash {
	if (e.hash == common.Hash{}) {
		encoded, _ := rlp.EncodeToBytes(e)
		e.hash = crypto.Keccak256Hash(encoded)
	}
	return e.hash
}

func (e *Envelope) DecodeRLP(s *rlp.Stream) error {
	raw, err := s.Raw()
	if err != nil {
		return err
	}
	// The decoding of Envelope uses the struct fields but also needs
	// to compute the hash of the whole RLP-encoded envelope. This
	// type has the same structure as Envelope but is not an
	// rlp.Decoder (does not implement DecodeRLP function).
	// Only public members will be encoded.
	type rlpenv Envelope
	if err := rlp.DecodeBytes(raw, (*rlpenv)(e)); err != nil {
		return err
	}
	e.hash = crypto.Keccak256Hash(raw)
	return nil
}

func (e *Envelope) OpenAsymmetric(key *ecdsa.PrivateKey) (*ReceivedMessage, error) {
	message := &ReceivedMessage{Raw: e.Data}
	err := message.decryptAsymmetric(key)
	switch err {
	case nil:
		return message, nil
	case ecies.ErrInvalidPublicKey: // addressed to somebody else
		return nil, err
	default:
		return nil, fmt.Errorf("unable to open envelope, decrypt failed: %v", err)
	}
}

func (e *Envelope) OpenSymmetric(key []byte) (msg *ReceivedMessage, err error) {
	msg = &ReceivedMessage{Raw: e.Data}
	err = msg.decryptSymmetric(key, e.AESNonce)
	if err != nil {
		msg = nil
	}
	return msg, err
}

func (e *Envelope) Open(watcher *Filter) (msg *ReceivedMessage) {
	if e.isAsymmetric() {
		msg, _ = e.OpenAsymmetric(watcher.KeyAsym)
		if msg != nil {
			msg.Dst = &watcher.KeyAsym.PublicKey
		}
	} else if e.IsSymmetric() {
		msg, _ = e.OpenSymmetric(watcher.KeySym)
		if msg != nil {
			msg.SymKeyHash = crypto.Keccak256Hash(watcher.KeySym)
		}
	}

	if msg != nil {
		ok := msg.Validate()
		if !ok {
			return nil
		}
		msg.Topic = e.Topic
		msg.PoW = e.PoW()
		msg.TTL = e.TTL
		msg.Sent = e.Expiry - e.TTL
		msg.EnvelopeHash = e.Hash()
		msg.EnvelopeVersion = e.Ver()
	}
	return msg
}
