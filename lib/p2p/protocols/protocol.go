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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/metrics"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/lib/swarm/spancontext"
	"github.com/5uwifi/canchain/lib/swarm/tracing"
	opentracing "github.com/opentracing/opentracing-go"
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

type WrappedMsg struct {
	Context []byte
	Size    uint32
	Payload []byte
}

type Hook interface {
	Send(peer *Peer, size uint32, msg interface{}) error
	Receive(peer *Peer, size uint32, msg interface{}) error
}

type Spec struct {
	Name string

	Version uint

	MaxMsgSize uint32

	Messages []interface{}

	Hook Hook

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
	*p2p.Peer
	rw   p2p.MsgReadWriter
	spec *Spec
}

func NewPeer(p *p2p.Peer, rw p2p.MsgReadWriter, spec *Spec) *Peer {
	return &Peer{
		Peer: p,
		rw:   rw,
		spec: spec,
	}
}

func (p *Peer) Run(handler func(ctx context.Context, msg interface{}) error) error {
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

func (p *Peer) Send(ctx context.Context, msg interface{}) error {
	defer metrics.GetOrRegisterResettingTimer("peer.send_t", nil).UpdateSince(time.Now())
	metrics.GetOrRegisterCounter("peer.send", nil).Inc(1)

	var b bytes.Buffer
	if tracing.Enabled {
		writer := bufio.NewWriter(&b)

		tracer := opentracing.GlobalTracer()

		sctx := spancontext.FromContext(ctx)

		if sctx != nil {
			err := tracer.Inject(
				sctx,
				opentracing.Binary,
				writer)
			if err != nil {
				return err
			}
		}

		writer.Flush()
	}

	r, err := rlp.EncodeToBytes(msg)
	if err != nil {
		return err
	}

	wmsg := WrappedMsg{
		Context: b.Bytes(),
		Size:    uint32(len(r)),
		Payload: r,
	}

	if p.spec.Hook != nil {
		err := p.spec.Hook.Send(p, wmsg.Size, msg)
		if err != nil {
			p.Drop(err)
			return err
		}
	}

	code, found := p.spec.GetCode(msg)
	if !found {
		return errorf(ErrInvalidMsgType, "%v", code)
	}
	return p2p.Send(p.rw, code, wmsg)
}

func (p *Peer) handleIncoming(handle func(ctx context.Context, msg interface{}) error) error {
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	defer msg.Discard()

	if msg.Size > p.spec.MaxMsgSize {
		return errorf(ErrMsgTooLong, "%v > %v", msg.Size, p.spec.MaxMsgSize)
	}

	var wmsg WrappedMsg
	err = msg.Decode(&wmsg)
	if err != nil {
		log4j.Error(err.Error())
		return err
	}

	ctx := context.Background()

	if tracing.Enabled && len(wmsg.Context) > 0 {
		var sctx opentracing.SpanContext

		tracer := opentracing.GlobalTracer()
		sctx, err = tracer.Extract(
			opentracing.Binary,
			bytes.NewReader(wmsg.Context))
		if err != nil {
			log4j.Error(err.Error())
			return err
		}

		ctx = spancontext.WithContext(ctx, sctx)
	}

	val, ok := p.spec.NewMsg(msg.Code)
	if !ok {
		return errorf(ErrInvalidMsgCode, "%v", msg.Code)
	}
	if err := rlp.DecodeBytes(wmsg.Payload, val); err != nil {
		return errorf(ErrDecode, "<= %v: %v", msg, err)
	}

	if p.spec.Hook != nil {
		err := p.spec.Hook.Receive(p, wmsg.Size, val)
		if err != nil {
			return err
		}
	}

	if err := handle(ctx, val); err != nil {
		return errorf(ErrHandler, "(msg code %v): %v", msg.Code, err)
	}
	return nil
}

func (p *Peer) Handshake(ctx context.Context, hs interface{}, verify func(interface{}) error) (rhs interface{}, err error) {
	if _, ok := p.spec.GetCode(hs); !ok {
		return nil, errorf(ErrHandshake, "unknown handshake message type: %T", hs)
	}
	errc := make(chan error, 2)
	handle := func(ctx context.Context, msg interface{}) error {
		rhs = msg
		if verify != nil {
			return verify(rhs)
		}
		return nil
	}
	send := func() { errc <- p.Send(ctx, hs) }
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
