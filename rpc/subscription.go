package rpc

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrNotificationsUnsupported = errors.New("notifications not supported")
	ErrSubscriptionNotFound = errors.New("subscription not found")
)

type ID string

type Subscription struct {
	ID        ID
	namespace string
	err       chan error
}

func (s *Subscription) Err() <-chan error {
	return s.err
}

type notifierKey struct{}

type Notifier struct {
	codec    ServerCodec
	subMu    sync.Mutex
	active   map[ID]*Subscription
	inactive map[ID]*Subscription
	buffer   map[ID][]interface{}
}

func newNotifier(codec ServerCodec) *Notifier {
	return &Notifier{
		codec:    codec,
		active:   make(map[ID]*Subscription),
		inactive: make(map[ID]*Subscription),
		buffer:   make(map[ID][]interface{}),
	}
}

func NotifierFromContext(ctx context.Context) (*Notifier, bool) {
	n, ok := ctx.Value(notifierKey{}).(*Notifier)
	return n, ok
}

func (n *Notifier) CreateSubscription() *Subscription {
	s := &Subscription{ID: NewID(), err: make(chan error)}
	n.subMu.Lock()
	n.inactive[s.ID] = s
	n.subMu.Unlock()
	return s
}

func (n *Notifier) Notify(id ID, data interface{}) error {
	n.subMu.Lock()
	defer n.subMu.Unlock()

	if sub, active := n.active[id]; active {
		n.send(sub, data)
	} else {
		n.buffer[id] = append(n.buffer[id], data)
	}
	return nil
}

func (n *Notifier) send(sub *Subscription, data interface{}) error {
	notification := n.codec.CreateNotification(string(sub.ID), sub.namespace, data)
	err := n.codec.Write(notification)
	if err != nil {
		n.codec.Close()
	}
	return err
}

func (n *Notifier) Closed() <-chan interface{} {
	return n.codec.Closed()
}

func (n *Notifier) unsubscribe(id ID) error {
	n.subMu.Lock()
	defer n.subMu.Unlock()
	if s, found := n.active[id]; found {
		close(s.err)
		delete(n.active, id)
		return nil
	}
	return ErrSubscriptionNotFound
}

func (n *Notifier) activate(id ID, namespace string) {
	n.subMu.Lock()
	defer n.subMu.Unlock()

	if sub, found := n.inactive[id]; found {
		sub.namespace = namespace
		n.active[id] = sub
		delete(n.inactive, id)
		for _, data := range n.buffer[id] {
			n.send(sub, data)
		}
		delete(n.buffer, id)
	}
}
