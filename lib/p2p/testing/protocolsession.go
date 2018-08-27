package testing

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/lib/p2p/simulations/adapters"
)

var errTimedOut = errors.New("timed out")

type ProtocolSession struct {
	Server  *p2p.Server
	IDs     []discover.NodeID
	adapter *adapters.SimAdapter
	events  chan *p2p.PeerEvent
}

type Exchange struct {
	Label    string
	Triggers []Trigger
	Expects  []Expect
	Timeout  time.Duration
}

type Trigger struct {
	Msg     interface{}
	Code    uint64
	Peer    discover.NodeID
	Timeout time.Duration
}

type Expect struct {
	Msg     interface{}
	Code    uint64
	Peer    discover.NodeID
	Timeout time.Duration
}

type Disconnect struct {
	Peer  discover.NodeID
	Error error
}

func (s *ProtocolSession) trigger(trig Trigger) error {
	simNode, ok := s.adapter.GetNode(trig.Peer)
	if !ok {
		return fmt.Errorf("trigger: peer %v does not exist (1- %v)", trig.Peer, len(s.IDs))
	}
	mockNode, ok := simNode.Services()[0].(*mockNode)
	if !ok {
		return fmt.Errorf("trigger: peer %v is not a mock", trig.Peer)
	}

	errc := make(chan error)

	go func() {
		log4j.Trace(fmt.Sprintf("trigger %v (%v)....", trig.Msg, trig.Code))
		errc <- mockNode.Trigger(&trig)
		log4j.Trace(fmt.Sprintf("triggered %v (%v)", trig.Msg, trig.Code))
	}()

	t := trig.Timeout
	if t == time.Duration(0) {
		t = 1000 * time.Millisecond
	}
	select {
	case err := <-errc:
		return err
	case <-time.After(t):
		return fmt.Errorf("timout expecting %v to send to peer %v", trig.Msg, trig.Peer)
	}
}

func (s *ProtocolSession) expect(exps []Expect) error {
	peerExpects := make(map[discover.NodeID][]Expect)
	for _, exp := range exps {
		if exp.Msg == nil {
			return errors.New("no message to expect")
		}
		peerExpects[exp.Peer] = append(peerExpects[exp.Peer], exp)
	}

	mockNodes := make(map[discover.NodeID]*mockNode)
	for nodeID := range peerExpects {
		simNode, ok := s.adapter.GetNode(nodeID)
		if !ok {
			return fmt.Errorf("trigger: peer %v does not exist (1- %v)", nodeID, len(s.IDs))
		}
		mockNode, ok := simNode.Services()[0].(*mockNode)
		if !ok {
			return fmt.Errorf("trigger: peer %v is not a mock", nodeID)
		}
		mockNodes[nodeID] = mockNode
	}

	done := make(chan struct{})
	defer close(done)
	errc := make(chan error)

	wg := &sync.WaitGroup{}
	wg.Add(len(mockNodes))
	for nodeID, mockNode := range mockNodes {
		nodeID := nodeID
		mockNode := mockNode
		go func() {
			defer wg.Done()

			var t time.Duration
			for _, exp := range peerExpects[nodeID] {
				if exp.Timeout == time.Duration(0) {
					t += 2000 * time.Millisecond
				} else {
					t += exp.Timeout
				}
			}
			alarm := time.NewTimer(t)
			defer alarm.Stop()

			expectErrc := make(chan error)
			go func() {
				select {
				case expectErrc <- mockNode.Expect(peerExpects[nodeID]...):
				case <-done:
				case <-alarm.C:
				}
			}()

			select {
			case err := <-expectErrc:
				if err != nil {
					select {
					case errc <- err:
					case <-done:
					case <-alarm.C:
						errc <- errTimedOut
					}
				}
			case <-done:
			case <-alarm.C:
				errc <- errTimedOut
			}

		}()
	}

	go func() {
		wg.Wait()
		close(errc)
	}()

	return <-errc
}

func (s *ProtocolSession) TestExchanges(exchanges ...Exchange) error {
	for i, e := range exchanges {
		if err := s.testExchange(e); err != nil {
			return fmt.Errorf("exchange #%d %q: %v", i, e.Label, err)
		}
		log4j.Trace(fmt.Sprintf("exchange #%d %q: run successfully", i, e.Label))
	}
	return nil
}

func (s *ProtocolSession) testExchange(e Exchange) error {
	errc := make(chan error)
	done := make(chan struct{})
	defer close(done)

	go func() {
		for _, trig := range e.Triggers {
			err := s.trigger(trig)
			if err != nil {
				errc <- err
				return
			}
		}

		select {
		case errc <- s.expect(e.Expects):
		case <-done:
		}
	}()

	t := e.Timeout
	if t == 0 {
		t = 2000 * time.Millisecond
	}
	alarm := time.NewTimer(t)
	select {
	case err := <-errc:
		return err
	case <-alarm.C:
		return errTimedOut
	}
}

func (s *ProtocolSession) TestDisconnected(disconnects ...*Disconnect) error {
	expects := make(map[discover.NodeID]error)
	for _, disconnect := range disconnects {
		expects[disconnect.Peer] = disconnect.Error
	}

	timeout := time.After(time.Second)
	for len(expects) > 0 {
		select {
		case event := <-s.events:
			if event.Type != p2p.PeerEventTypeDrop {
				continue
			}
			expectErr, ok := expects[event.Peer]
			if !ok {
				continue
			}

			if !(expectErr == nil && event.Error == "" || expectErr != nil && expectErr.Error() == event.Error) {
				return fmt.Errorf("unexpected error on peer %v. expected '%v', got '%v'", event.Peer, expectErr, event.Error)
			}
			delete(expects, event.Peer)
		case <-timeout:
			return fmt.Errorf("timed out waiting for peers to disconnect")
		}
	}
	return nil
}
