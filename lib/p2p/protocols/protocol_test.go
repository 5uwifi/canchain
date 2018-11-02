package protocols

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/5uwifi/canchain/lib/rlp"

	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/simulations/adapters"
	p2ptest "github.com/5uwifi/canchain/lib/p2p/testing"
)

type hs0 struct {
	C uint
}

type kill struct {
	C cnode.ID
}

type drop struct {
}

type protoHandshake struct {
	Version   uint
	NetworkID string
}

func checkProtoHandshake(testVersion uint, testNetworkID string) func(interface{}) error {
	return func(rhs interface{}) error {
		remote := rhs.(*protoHandshake)
		if remote.NetworkID != testNetworkID {
			return fmt.Errorf("%s (!= %s)", remote.NetworkID, testNetworkID)
		}

		if remote.Version != testVersion {
			return fmt.Errorf("%d (!= %d)", remote.Version, testVersion)
		}
		return nil
	}
}

func newProtocol(pp *p2ptest.TestPeerPool) func(*p2p.Peer, p2p.MsgReadWriter) error {
	spec := &Spec{
		Name:       "test",
		Version:    42,
		MaxMsgSize: 10 * 1024,
		Messages: []interface{}{
			protoHandshake{},
			hs0{},
			kill{},
			drop{},
		},
	}
	return func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
		peer := NewPeer(p, rw, spec)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		phs := &protoHandshake{42, "420"}
		hsCheck := checkProtoHandshake(phs.Version, phs.NetworkID)
		_, err := peer.Handshake(ctx, phs, hsCheck)
		if err != nil {
			return err
		}

		lhs := &hs0{42}
		hs, err := peer.Handshake(ctx, lhs, nil)
		if err != nil {
			return err
		}

		if rmhs := hs.(*hs0); rmhs.C > lhs.C {
			return fmt.Errorf("handshake mismatch remote %v > local %v", rmhs.C, lhs.C)
		}

		handle := func(ctx context.Context, msg interface{}) error {
			switch msg := msg.(type) {

			case *protoHandshake:
				return errors.New("duplicate handshake")

			case *hs0:
				rhs := msg
				if rhs.C > lhs.C {
					return fmt.Errorf("handshake mismatch remote %v > local %v", rhs.C, lhs.C)
				}
				lhs.C += rhs.C
				return peer.Send(ctx, lhs)

			case *kill:
				id := msg.C
				pp.Get(id).Drop(errors.New("killed"))
				return nil

			case *drop:
				return errors.New("dropped")

			default:
				return fmt.Errorf("unknown message type: %T", msg)
			}
		}

		pp.Add(peer)
		defer pp.Remove(peer)
		return peer.Run(handle)
	}
}

func protocolTester(t *testing.T, pp *p2ptest.TestPeerPool) *p2ptest.ProtocolTester {
	conf := adapters.RandomNodeConfig()
	return p2ptest.NewProtocolTester(t, conf.ID, 2, newProtocol(pp))
}

func protoHandshakeExchange(id cnode.ID, proto *protoHandshake) []p2ptest.Exchange {

	return []p2ptest.Exchange{
		{
			Expects: []p2ptest.Expect{
				{
					Code: 0,
					Msg:  &protoHandshake{42, "420"},
					Peer: id,
				},
			},
		},
		{
			Triggers: []p2ptest.Trigger{
				{
					Code: 0,
					Msg:  proto,
					Peer: id,
				},
			},
		},
	}
}

func runProtoHandshake(t *testing.T, proto *protoHandshake, errs ...error) {
	pp := p2ptest.NewTestPeerPool()
	s := protocolTester(t, pp)
	node := s.Nodes[0]
	if err := s.TestExchanges(protoHandshakeExchange(node.ID(), proto)...); err != nil {
		t.Fatal(err)
	}
	var disconnects []*p2ptest.Disconnect
	for i, err := range errs {
		disconnects = append(disconnects, &p2ptest.Disconnect{Peer: s.Nodes[i].ID(), Error: err})
	}
	if err := s.TestDisconnected(disconnects...); err != nil {
		t.Fatal(err)
	}
}

type dummyHook struct {
	peer  *Peer
	size  uint32
	msg   interface{}
	send  bool
	err   error
	waitC chan struct{}
}

type dummyMsg struct {
	Content string
}

func (d *dummyHook) Send(peer *Peer, size uint32, msg interface{}) error {
	d.peer = peer
	d.size = size
	d.msg = msg
	d.send = true
	return d.err
}

func (d *dummyHook) Receive(peer *Peer, size uint32, msg interface{}) error {
	d.peer = peer
	d.size = size
	d.msg = msg
	d.send = false
	d.waitC <- struct{}{}
	return d.err
}

func TestProtocolHook(t *testing.T) {
	testHook := &dummyHook{
		waitC: make(chan struct{}, 1),
	}
	spec := &Spec{
		Name:       "test",
		Version:    42,
		MaxMsgSize: 10 * 1024,
		Messages: []interface{}{
			dummyMsg{},
		},
		Hook: testHook,
	}

	runFunc := func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
		peer := NewPeer(p, rw, spec)
		ctx := context.TODO()
		err := peer.Send(ctx, &dummyMsg{
			Content: "handshake"})

		if err != nil {
			t.Fatal(err)
		}

		handle := func(ctx context.Context, msg interface{}) error {
			return nil
		}

		return peer.Run(handle)
	}

	conf := adapters.RandomNodeConfig()
	tester := p2ptest.NewProtocolTester(t, conf.ID, 2, runFunc)
	err := tester.TestExchanges(p2ptest.Exchange{
		Expects: []p2ptest.Expect{
			{
				Code: 0,
				Msg:  &dummyMsg{Content: "handshake"},
				Peer: tester.Nodes[0].ID(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if testHook.msg == nil || testHook.msg.(*dummyMsg).Content != "handshake" {
		t.Fatal("Expected msg to be set, but it is not")
	}
	if !testHook.send {
		t.Fatal("Expected a send message, but it is not")
	}
	if testHook.peer == nil || testHook.peer.ID() != tester.Nodes[0].ID() {
		t.Fatal("Expected peer ID to be set correctly, but it is not")
	}
	if testHook.size != 11 {
		t.Fatalf("Expected size to be %d, but it is %d ", 1, testHook.size)
	}

	err = tester.TestExchanges(p2ptest.Exchange{
		Triggers: []p2ptest.Trigger{
			{
				Code: 0,
				Msg:  &dummyMsg{Content: "response"},
				Peer: tester.Nodes[1].ID(),
			},
		},
	})

	<-testHook.waitC

	if err != nil {
		t.Fatal(err)
	}
	if testHook.msg == nil || testHook.msg.(*dummyMsg).Content != "response" {
		t.Fatal("Expected msg to be set, but it is not")
	}
	if testHook.send {
		t.Fatal("Expected a send message, but it is not")
	}
	if testHook.peer == nil || testHook.peer.ID() != tester.Nodes[1].ID() {
		t.Fatal("Expected peer ID to be set correctly, but it is not")
	}
	if testHook.size != 10 {
		t.Fatalf("Expected size to be %d, but it is %d ", 1, testHook.size)
	}

	testHook.err = fmt.Errorf("dummy error")
	err = tester.TestExchanges(p2ptest.Exchange{
		Triggers: []p2ptest.Trigger{
			{
				Code: 0,
				Msg:  &dummyMsg{Content: "response"},
				Peer: tester.Nodes[1].ID(),
			},
		},
	})

	<-testHook.waitC

	time.Sleep(100 * time.Millisecond)
	err = tester.TestDisconnected(&p2ptest.Disconnect{tester.Nodes[1].ID(), testHook.err})
	if err != nil {
		t.Fatalf("Expected a specific disconnect error, but got different one: %v", err)
	}

}

func TestNoHook(t *testing.T) {
	spec := createTestSpec()
	id := adapters.RandomNodeConfig().ID
	p := p2p.NewPeer(id, "testPeer", nil)
	rw := &dummyRW{}
	peer := NewPeer(p, rw, spec)
	ctx := context.TODO()
	msg := &perBytesMsgSenderPays{Content: "testBalance"}
	err := peer.Send(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	rw.msg = msg
	peer.handleIncoming(func(ctx context.Context, msg interface{}) error {
		return nil
	})
}

func TestProtoHandshakeVersionMismatch(t *testing.T) {
	runProtoHandshake(t, &protoHandshake{41, "420"}, errorf(ErrHandshake, errorf(ErrHandler, "(msg code 0): 41 (!= 42)").Error()))
}

func TestProtoHandshakeNetworkIDMismatch(t *testing.T) {
	runProtoHandshake(t, &protoHandshake{42, "421"}, errorf(ErrHandshake, errorf(ErrHandler, "(msg code 0): 421 (!= 420)").Error()))
}

func TestProtoHandshakeSuccess(t *testing.T) {
	runProtoHandshake(t, &protoHandshake{42, "420"})
}

func moduleHandshakeExchange(id cnode.ID, resp uint) []p2ptest.Exchange {

	return []p2ptest.Exchange{
		{
			Expects: []p2ptest.Expect{
				{
					Code: 1,
					Msg:  &hs0{42},
					Peer: id,
				},
			},
		},
		{
			Triggers: []p2ptest.Trigger{
				{
					Code: 1,
					Msg:  &hs0{resp},
					Peer: id,
				},
			},
		},
	}
}

func runModuleHandshake(t *testing.T, resp uint, errs ...error) {
	pp := p2ptest.NewTestPeerPool()
	s := protocolTester(t, pp)
	node := s.Nodes[0]
	if err := s.TestExchanges(protoHandshakeExchange(node.ID(), &protoHandshake{42, "420"})...); err != nil {
		t.Fatal(err)
	}
	if err := s.TestExchanges(moduleHandshakeExchange(node.ID(), resp)...); err != nil {
		t.Fatal(err)
	}
	var disconnects []*p2ptest.Disconnect
	for i, err := range errs {
		disconnects = append(disconnects, &p2ptest.Disconnect{Peer: s.Nodes[i].ID(), Error: err})
	}
	if err := s.TestDisconnected(disconnects...); err != nil {
		t.Fatal(err)
	}
}

func TestModuleHandshakeError(t *testing.T) {
	runModuleHandshake(t, 43, fmt.Errorf("handshake mismatch remote 43 > local 42"))
}

func TestModuleHandshakeSuccess(t *testing.T) {
	runModuleHandshake(t, 42)
}

func testMultiPeerSetup(a, b cnode.ID) []p2ptest.Exchange {

	return []p2ptest.Exchange{
		{
			Label: "primary handshake",
			Expects: []p2ptest.Expect{
				{
					Code: 0,
					Msg:  &protoHandshake{42, "420"},
					Peer: a,
				},
				{
					Code: 0,
					Msg:  &protoHandshake{42, "420"},
					Peer: b,
				},
			},
		},
		{
			Label: "module handshake",
			Triggers: []p2ptest.Trigger{
				{
					Code: 0,
					Msg:  &protoHandshake{42, "420"},
					Peer: a,
				},
				{
					Code: 0,
					Msg:  &protoHandshake{42, "420"},
					Peer: b,
				},
			},
			Expects: []p2ptest.Expect{
				{
					Code: 1,
					Msg:  &hs0{42},
					Peer: a,
				},
				{
					Code: 1,
					Msg:  &hs0{42},
					Peer: b,
				},
			},
		},

		{Label: "alternative module handshake", Triggers: []p2ptest.Trigger{{Code: 1, Msg: &hs0{41}, Peer: a},
			{Code: 1, Msg: &hs0{41}, Peer: b}}},
		{Label: "repeated module handshake", Triggers: []p2ptest.Trigger{{Code: 1, Msg: &hs0{1}, Peer: a}}},
		{Label: "receiving repeated module handshake", Expects: []p2ptest.Expect{{Code: 1, Msg: &hs0{43}, Peer: a}}}}
}

func runMultiplePeers(t *testing.T, peer int, errs ...error) {
	pp := p2ptest.NewTestPeerPool()
	s := protocolTester(t, pp)

	if err := s.TestExchanges(testMultiPeerSetup(s.Nodes[0].ID(), s.Nodes[1].ID())...); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	timeout := time.NewTimer(1 * time.Second)
WAIT:
	for {
		select {
		case <-tick.C:
			if pp.Has(s.Nodes[0].ID()) {
				break WAIT
			}
		case <-timeout.C:
			t.Fatal("timeout")
		}
	}
	if !pp.Has(s.Nodes[1].ID()) {
		t.Fatalf("missing peer test-1: %v (%v)", pp, s.Nodes)
	}

	err := s.TestExchanges(p2ptest.Exchange{
		Triggers: []p2ptest.Trigger{
			{
				Code: 2,
				Msg:  &kill{s.Nodes[peer].ID()},
				Peer: s.Nodes[0].ID(),
			},
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	err = s.TestExchanges(p2ptest.Exchange{
		Triggers: []p2ptest.Trigger{
			{
				Code: 3,
				Msg:  &drop{},
				Peer: s.Nodes[(peer+1)%2].ID(),
			},
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	var disconnects []*p2ptest.Disconnect
	for i, err := range errs {
		disconnects = append(disconnects, &p2ptest.Disconnect{Peer: s.Nodes[i].ID(), Error: err})
	}
	if err := s.TestDisconnected(disconnects...); err != nil {
		t.Fatal(err)
	}
	if pp.Has(s.Nodes[peer].ID()) {
		t.Fatalf("peer test-%v not dropped: %v (%v)", peer, pp, s.Nodes)
	}

}
func XTestMultiplePeersDropSelf(t *testing.T) {
	runMultiplePeers(t, 0,
		fmt.Errorf("subprotocol error"),
		fmt.Errorf("Message handler error: (msg code 3): dropped"),
	)
}

func XTestMultiplePeersDropOther(t *testing.T) {
	runMultiplePeers(t, 1,
		fmt.Errorf("Message handler error: (msg code 3): dropped"),
		fmt.Errorf("subprotocol error"),
	)
}

type dummyRW struct {
	msg  interface{}
	size uint32
	code uint64
}

func (d *dummyRW) WriteMsg(msg p2p.Msg) error {
	return nil
}

func (d *dummyRW) ReadMsg() (p2p.Msg, error) {
	enc := bytes.NewReader(d.getDummyMsg())
	return p2p.Msg{
		Code:       d.code,
		Size:       d.size,
		Payload:    enc,
		ReceivedAt: time.Now(),
	}, nil
}

func (d *dummyRW) getDummyMsg() []byte {
	r, _ := rlp.EncodeToBytes(d.msg)
	var b bytes.Buffer
	wmsg := WrappedMsg{
		Context: b.Bytes(),
		Size:    uint32(len(r)),
		Payload: r,
	}
	rr, _ := rlp.EncodeToBytes(wmsg)
	d.size = uint32(len(rr))
	return rr
}
