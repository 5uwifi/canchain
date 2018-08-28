package lcs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common/mclock"
	"github.com/5uwifi/canchain/light"
)

var (
	retryQueue         = time.Millisecond * 100
	softRequestTimeout = time.Millisecond * 500
	hardRequestTimeout = time.Second * 10
)

type retrieveManager struct {
	dist       *requestDistributor
	peers      *peerSet
	serverPool peerSelector

	lock     sync.RWMutex
	sentReqs map[uint64]*sentReq
}

type validatorFunc func(distPeer, *Msg) error

type peerSelector interface {
	adjustResponseTime(*poolEntry, time.Duration, bool)
}

type sentReq struct {
	rm       *retrieveManager
	req      *distReq
	id       uint64
	validate validatorFunc

	eventsCh chan reqPeerEvent
	stopCh   chan struct{}
	stopped  bool
	err      error

	lock   sync.RWMutex
	sentTo map[distPeer]sentReqToPeer

	lastReqQueued bool
	lastReqSentTo distPeer
	reqSrtoCount  int
}

type sentReqToPeer struct {
	delivered bool
	valid     chan bool
}

type reqPeerEvent struct {
	event int
	peer  distPeer
}

const (
	rpSent = iota
	rpSoftTimeout
	rpHardTimeout
	rpDeliveredValid
	rpDeliveredInvalid
)

func newRetrieveManager(peers *peerSet, dist *requestDistributor, serverPool peerSelector) *retrieveManager {
	return &retrieveManager{
		peers:      peers,
		dist:       dist,
		serverPool: serverPool,
		sentReqs:   make(map[uint64]*sentReq),
	}
}

func (rm *retrieveManager) retrieve(ctx context.Context, reqID uint64, req *distReq, val validatorFunc, shutdown chan struct{}) error {
	sentReq := rm.sendReq(reqID, req, val)
	select {
	case <-sentReq.stopCh:
	case <-ctx.Done():
		sentReq.stop(ctx.Err())
	case <-shutdown:
		sentReq.stop(fmt.Errorf("Client is shutting down"))
	}
	return sentReq.getError()
}

func (rm *retrieveManager) sendReq(reqID uint64, req *distReq, val validatorFunc) *sentReq {
	r := &sentReq{
		rm:       rm,
		req:      req,
		id:       reqID,
		sentTo:   make(map[distPeer]sentReqToPeer),
		stopCh:   make(chan struct{}),
		eventsCh: make(chan reqPeerEvent, 10),
		validate: val,
	}

	canSend := req.canSend
	req.canSend = func(p distPeer) bool {
		r.lock.RLock()
		_, sent := r.sentTo[p]
		r.lock.RUnlock()
		return !sent && canSend(p)
	}

	request := req.request
	req.request = func(p distPeer) func() {
		r.lock.Lock()
		r.sentTo[p] = sentReqToPeer{false, make(chan bool, 1)}
		r.lock.Unlock()
		return request(p)
	}
	rm.lock.Lock()
	rm.sentReqs[reqID] = r
	rm.lock.Unlock()

	go r.retrieveLoop()
	return r
}

func (rm *retrieveManager) deliver(peer distPeer, msg *Msg) error {
	rm.lock.RLock()
	req, ok := rm.sentReqs[msg.ReqID]
	rm.lock.RUnlock()

	if ok {
		return req.deliver(peer, msg)
	}
	return errResp(ErrUnexpectedResponse, "reqID = %v", msg.ReqID)
}

type reqStateFn func() reqStateFn

func (r *sentReq) retrieveLoop() {
	go r.tryRequest()
	r.lastReqQueued = true
	state := r.stateRequesting

	for state != nil {
		state = state()
	}

	r.rm.lock.Lock()
	delete(r.rm.sentReqs, r.id)
	r.rm.lock.Unlock()
}

func (r *sentReq) stateRequesting() reqStateFn {
	select {
	case ev := <-r.eventsCh:
		r.update(ev)
		switch ev.event {
		case rpSent:
			if ev.peer == nil {
				if r.waiting() {
					return r.stateNoMorePeers
				}
				r.stop(light.ErrNoPeers)
				return nil
			}
		case rpSoftTimeout:
			go r.tryRequest()
			r.lastReqQueued = true
			return r.stateRequesting
		case rpDeliveredValid:
			r.stop(nil)
			return r.stateStopped
		}
		return r.stateRequesting
	case <-r.stopCh:
		return r.stateStopped
	}
}

func (r *sentReq) stateNoMorePeers() reqStateFn {
	select {
	case <-time.After(retryQueue):
		go r.tryRequest()
		r.lastReqQueued = true
		return r.stateRequesting
	case ev := <-r.eventsCh:
		r.update(ev)
		if ev.event == rpDeliveredValid {
			r.stop(nil)
			return r.stateStopped
		}
		return r.stateNoMorePeers
	case <-r.stopCh:
		return r.stateStopped
	}
}

func (r *sentReq) stateStopped() reqStateFn {
	for r.waiting() {
		r.update(<-r.eventsCh)
	}
	return nil
}

func (r *sentReq) update(ev reqPeerEvent) {
	switch ev.event {
	case rpSent:
		r.lastReqQueued = false
		r.lastReqSentTo = ev.peer
	case rpSoftTimeout:
		r.lastReqSentTo = nil
		r.reqSrtoCount++
	case rpHardTimeout:
		r.reqSrtoCount--
	case rpDeliveredValid, rpDeliveredInvalid:
		if ev.peer == r.lastReqSentTo {
			r.lastReqSentTo = nil
		} else {
			r.reqSrtoCount--
		}
	}
}

func (r *sentReq) waiting() bool {
	return r.lastReqQueued || r.lastReqSentTo != nil || r.reqSrtoCount > 0
}

func (r *sentReq) tryRequest() {
	sent := r.rm.dist.queue(r.req)
	var p distPeer
	select {
	case p = <-sent:
	case <-r.stopCh:
		if r.rm.dist.cancel(r.req) {
			p = nil
		} else {
			p = <-sent
		}
	}

	r.eventsCh <- reqPeerEvent{rpSent, p}
	if p == nil {
		return
	}

	reqSent := mclock.Now()
	srto, hrto := false, false

	r.lock.RLock()
	s, ok := r.sentTo[p]
	r.lock.RUnlock()
	if !ok {
		panic(nil)
	}

	defer func() {
		pp, ok := p.(*peer)
		if ok && r.rm.serverPool != nil {
			respTime := time.Duration(mclock.Now() - reqSent)
			r.rm.serverPool.adjustResponseTime(pp.poolEntry, respTime, srto)
		}
		if hrto {
			pp.Log().Debug("Request timed out hard")
			if r.rm.peers != nil {
				r.rm.peers.Unregister(pp.id)
			}
		}

		r.lock.Lock()
		delete(r.sentTo, p)
		r.lock.Unlock()
	}()

	select {
	case ok := <-s.valid:
		if ok {
			r.eventsCh <- reqPeerEvent{rpDeliveredValid, p}
		} else {
			r.eventsCh <- reqPeerEvent{rpDeliveredInvalid, p}
		}
		return
	case <-time.After(softRequestTimeout):
		srto = true
		r.eventsCh <- reqPeerEvent{rpSoftTimeout, p}
	}

	select {
	case ok := <-s.valid:
		if ok {
			r.eventsCh <- reqPeerEvent{rpDeliveredValid, p}
		} else {
			r.eventsCh <- reqPeerEvent{rpDeliveredInvalid, p}
		}
	case <-time.After(hardRequestTimeout):
		hrto = true
		r.eventsCh <- reqPeerEvent{rpHardTimeout, p}
	}
}

func (r *sentReq) deliver(peer distPeer, msg *Msg) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	s, ok := r.sentTo[peer]
	if !ok || s.delivered {
		return errResp(ErrUnexpectedResponse, "reqID = %v", msg.ReqID)
	}
	valid := r.validate(peer, msg) == nil
	r.sentTo[peer] = sentReqToPeer{true, s.valid}
	s.valid <- valid
	if !valid {
		return errResp(ErrInvalidResponse, "reqID = %v", msg.ReqID)
	}
	return nil
}

func (r *sentReq) stop(err error) {
	r.lock.Lock()
	if !r.stopped {
		r.stopped = true
		r.err = err
		close(r.stopCh)
	}
	r.lock.Unlock()
}

func (r *sentReq) getError() error {
	return r.err
}

func genReqID() uint64 {
	var rnd [8]byte
	rand.Read(rnd[:])
	return binary.BigEndian.Uint64(rnd[:])
}
