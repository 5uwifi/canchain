package lcs

import (
	"container/list"
	"errors"
	"sync"
	"time"
)

var ErrNoPeers = errors.New("no suitable peers available")

type requestDistributor struct {
	reqQueue         *list.List
	lastReqOrder     uint64
	peers            map[distPeer]struct{}
	peerLock         sync.RWMutex
	stopChn, loopChn chan struct{}
	loopNextSent     bool
	lock             sync.Mutex
}

type distPeer interface {
	waitBefore(uint64) (time.Duration, float64)
	canQueue() bool
	queueSend(f func())
}

type distReq struct {
	getCost func(distPeer) uint64
	canSend func(distPeer) bool
	request func(distPeer) func()

	reqOrder uint64
	sentChn  chan distPeer
	element  *list.Element
}

func newRequestDistributor(peers *peerSet, stopChn chan struct{}) *requestDistributor {
	d := &requestDistributor{
		reqQueue: list.New(),
		loopChn:  make(chan struct{}, 2),
		stopChn:  stopChn,
		peers:    make(map[distPeer]struct{}),
	}
	if peers != nil {
		peers.notify(d)
	}
	go d.loop()
	return d
}

func (d *requestDistributor) registerPeer(p *peer) {
	d.peerLock.Lock()
	d.peers[p] = struct{}{}
	d.peerLock.Unlock()
}

func (d *requestDistributor) unregisterPeer(p *peer) {
	d.peerLock.Lock()
	delete(d.peers, p)
	d.peerLock.Unlock()
}

func (d *requestDistributor) registerTestPeer(p distPeer) {
	d.peerLock.Lock()
	d.peers[p] = struct{}{}
	d.peerLock.Unlock()
}

const distMaxWait = time.Millisecond * 10

func (d *requestDistributor) loop() {
	for {
		select {
		case <-d.stopChn:
			d.lock.Lock()
			elem := d.reqQueue.Front()
			for elem != nil {
				close(elem.Value.(*distReq).sentChn)
				elem = elem.Next()
			}
			d.lock.Unlock()
			return
		case <-d.loopChn:
			d.lock.Lock()
			d.loopNextSent = false
		loop:
			for {
				peer, req, wait := d.nextRequest()
				if req != nil && wait == 0 {
					chn := req.sentChn
					d.remove(req)
					send := req.request(peer)
					if send != nil {
						peer.queueSend(send)
					}
					chn <- peer
					close(chn)
				} else {
					if wait == 0 {
						break loop
					}
					d.loopNextSent = true
					if wait > distMaxWait {
						wait = distMaxWait
					}
					go func() {
						time.Sleep(wait)
						d.loopChn <- struct{}{}
					}()
					break loop
				}
			}
			d.lock.Unlock()
		}
	}
}

type selectPeerItem struct {
	peer   distPeer
	req    *distReq
	weight int64
}

func (sp selectPeerItem) Weight() int64 {
	return sp.weight
}

func (d *requestDistributor) nextRequest() (distPeer, *distReq, time.Duration) {
	checkedPeers := make(map[distPeer]struct{})
	elem := d.reqQueue.Front()
	var (
		bestPeer distPeer
		bestReq  *distReq
		bestWait time.Duration
		sel      *weightedRandomSelect
	)

	d.peerLock.RLock()
	defer d.peerLock.RUnlock()

	for (len(d.peers) > 0 || elem == d.reqQueue.Front()) && elem != nil {
		req := elem.Value.(*distReq)
		canSend := false
		for peer := range d.peers {
			if _, ok := checkedPeers[peer]; !ok && peer.canQueue() && req.canSend(peer) {
				canSend = true
				cost := req.getCost(peer)
				wait, bufRemain := peer.waitBefore(cost)
				if wait == 0 {
					if sel == nil {
						sel = newWeightedRandomSelect()
					}
					sel.update(selectPeerItem{peer: peer, req: req, weight: int64(bufRemain*1000000) + 1})
				} else {
					if bestReq == nil || wait < bestWait {
						bestPeer = peer
						bestReq = req
						bestWait = wait
					}
				}
				checkedPeers[peer] = struct{}{}
			}
		}
		next := elem.Next()
		if !canSend && elem == d.reqQueue.Front() {
			close(req.sentChn)
			d.remove(req)
		}
		elem = next
	}

	if sel != nil {
		c := sel.choose().(selectPeerItem)
		return c.peer, c.req, 0
	}
	return bestPeer, bestReq, bestWait
}

func (d *requestDistributor) queue(r *distReq) chan distPeer {
	d.lock.Lock()
	defer d.lock.Unlock()

	if r.reqOrder == 0 {
		d.lastReqOrder++
		r.reqOrder = d.lastReqOrder
	}

	back := d.reqQueue.Back()
	if back == nil || r.reqOrder > back.Value.(*distReq).reqOrder {
		r.element = d.reqQueue.PushBack(r)
	} else {
		before := d.reqQueue.Front()
		for before.Value.(*distReq).reqOrder < r.reqOrder {
			before = before.Next()
		}
		r.element = d.reqQueue.InsertBefore(r, before)
	}

	if !d.loopNextSent {
		d.loopNextSent = true
		d.loopChn <- struct{}{}
	}

	r.sentChn = make(chan distPeer, 1)
	return r.sentChn
}

func (d *requestDistributor) cancel(r *distReq) bool {
	d.lock.Lock()
	defer d.lock.Unlock()

	if r.sentChn == nil {
		return false
	}

	close(r.sentChn)
	d.remove(r)
	return true
}

func (d *requestDistributor) remove(r *distReq) {
	r.sentChn = nil
	if r.element != nil {
		d.reqQueue.Remove(r.element)
		r.element = nil
	}
}
