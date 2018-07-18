
/*
Package protocols is an extension to p2p. It offers a user friendly simple way to define
devp2p subprotocols by abstracting away code standardly shared by protocols.

* automate assigments of code indexes to messages
* automate RLP decoding/encoding based on reflecting
* provide the forever loop to read incoming messages
* standardise error handling related to communication
* standardised	handshake negotiation
* TODO: automatic generation of wire protocol specification for peers

*/
package protocols

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/metrics"
	"github.com/5uwifi/canchain/basis/p2p"
)

const (
	ErrMsgTooLong = iota
	ErrDecode
	ErrWrite
	ErrInvalidMsgCode
	ErrInvalidMsgType
	ErrHandshake
	ErrNoHandler
	ErrHandler
)

var errorToString = map[int]string{
	ErrMsgTooLong:     "Message too long",
	ErrDecode:         "Invalid message (RLP error)",
	ErrWrite:          "Error sending message",
	ErrInvalidMsgCode: "Invalid message code",
	ErrInvalidMsgType: "Invalid message type",
	ErrHandshake:      "Handshake error",
	ErrNoHandler:      "No handler registered error",
	ErrHandler:        "Message handler error",
}

/*
Error implements the standard go error interface.
Use:

  errorf(code, format, params ...interface{})

Prints as:

 <description>: <details>

where description is given by code in errorToString
and details is fmt.Sprintf(format, params...)

exported field Code can be checked
*/
type Error struct {
	Code    int
	message string
	format  string
	params  []interface{}
}

func (e Error) Error() (message string) {
	if len(e.message) == 0 {
		name, ok := errorToString[e.Code]
		if !ok {
			panic("invalid message code")
		}
		e.message = name
		if e.format != "" {
			e.message += ": " + fmt.Sprintf(e.format, e.params...)
		}
	}
	return e.message
}

func errorf(code int, format string, params ...interface{}) *Error {
	return &Error{
		Code:   code,
		format: format,
		params: params,
	}
}

type Spec struct {
	// Name is the name of the protocol, often a three-letter word
	Name string

	// Version is the version number of the protocol
	Version uint

	// MaxMsgSize is the maximum accepted length of the message payload
	MaxMsgSize uint32

	// Messages is a list of message data types which this protocol uses, with
	// each message type being sent with its array index as the code (so
	// [&foo{}, &bar{}, &baz{}] would send foo, bar and baz with codes
	// 0, 1 and 2 respectively)
	// each message must have a single unique data type
	Messages []interface{}

	initOnce sync.Once
	codes    map[reflect.Type]uint64
	types    map[uint64]reflect.Type
}

func (s *Spec) init() {
	s.initOnce.Do(func() {
		s.codes = make(map[reflect.Type]uint64, len(s.Messages))
		s.types = make(map[uint64]reflect.Type, len(s.Messages))
		for i, msg := range s.Messages {
			code := uint64(i)
			typ := reflect.TypeOf(msg)
			if typ.Kind() == reflect.Ptr {
				typ = typ.Elem()
			}
			s.codes[typ] = code
			s.types[code] = typ
		}
	})
}

func (s *Spec) Length() uint64 {
	return uint64(len(s.Messages))
}

func (s *Spec) GetCode(msg interface{}) (uint64, bool) {
	s.init()
	typ := reflect.TypeOf(msg)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	code, ok := s.codes[typ]
	return code, ok
}

func (s *Spec) NewMsg(code uint64) (interface{}, bool) {
	s.init()
	typ, ok := s.types[code]
	if !ok {
		return nil, false
	}
	return reflect.New(typ).Interface(), true
}

type Peer struct {
	*p2p.Peer                   // the p2p.Peer object representing the remote
	rw        p2p.MsgReadWriter // p2p.MsgReadWriter to send messages to and read messages from
	spec      *Spec
}

func NewPeer(p *p2p.Peer, rw p2p.MsgReadWriter, spec *Spec) *Peer {
	return &Peer{
		Peer: p,
		rw:   rw,
		spec: spec,
	}
}

func (p *Peer) Run(handler func(msg interface{}) error) error {
	for {
		if err := p.handleIncoming(handler); err != nil {
			if err != io.EOF {
				metrics.GetOrRegisterCounter("peer.handleincoming.error", nil).Inc(1)
				log4j.Error("peer.handleIncoming", "err", err)
			}

			return err
		}
	}
}

func (p *Peer) Drop(err error) {
	p.Disconnect(p2p.DiscSubprotocolError)
}

func (p *Peer) Send(msg interface{}) error {
	defer metrics.GetOrRegisterResettingTimer("peer.send_t", nil).UpdateSince(time.Now())
	metrics.GetOrRegisterCounter("peer.send", nil).Inc(1)
	code, found := p.spec.GetCode(msg)
	if !found {
		return errorf(ErrInvalidMsgType, "%v", code)
	}
	return p2p.Send(p.rw, code, msg)
}

// * checks message size,
// * checks for out-of-range message codes,
// * handles decoding with reflection,
// * call handlers as callbacks
func (p *Peer) handleIncoming(handle func(msg interface{}) error) error {
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	// make sure that the payload has been fully consumed
	defer msg.Discard()

	if msg.Size > p.spec.MaxMsgSize {
		return errorf(ErrMsgTooLong, "%v > %v", msg.Size, p.spec.MaxMsgSize)
	}

	val, ok := p.spec.NewMsg(msg.Code)
	if !ok {
		return errorf(ErrInvalidMsgCode, "%v", msg.Code)
	}
	if err := msg.Decode(val); err != nil {
		return errorf(ErrDecode, "<= %v: %v", msg, err)
	}

	// call the registered handler callbacks
	// a registered callback take the decoded message as argument as an interface
	// which the handler is supposed to cast to the appropriate type
	// it is entirely safe not to check the cast in the handler since the handler is
	// chosen based on the proper type in the first place
	if err := handle(val); err != nil {
		return errorf(ErrHandler, "(msg code %v): %v", msg.Code, err)
	}
	return nil
}

// * arguments
//   * context
//   * the local handshake to be sent to the remote peer
//   * funcion to be called on the remote handshake (can be nil)
// * expects a remote handshake back of the same type
// * the dialing peer needs to send the handshake first and then waits for remote
// * the listening peer waits for the remote handshake and then sends it
func (p *Peer) Handshake(ctx context.Context, hs interface{}, verify func(interface{}) error) (rhs interface{}, err error) {
	if _, ok := p.spec.GetCode(hs); !ok {
		return nil, errorf(ErrHandshake, "unknown handshake message type: %T", hs)
	}
	errc := make(chan error, 2)
	handle := func(msg interface{}) error {
		rhs = msg
		if verify != nil {
			return verify(rhs)
		}
		return nil
	}
	send := func() { errc <- p.Send(hs) }
	receive := func() { errc <- p.handleIncoming(handle) }

	go func() {
		if p.Inbound() {
			receive()
			send()
		} else {
			send()
			receive()
		}
	}()

	for i := 0; i < 2; i++ {
		select {
		case err = <-errc:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err != nil {
			return nil, errorf(ErrHandshake, err.Error())
		}
	}
	return rhs, nil
}
