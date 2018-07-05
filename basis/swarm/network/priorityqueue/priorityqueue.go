//
// (at your option) any later version.
//
//


package priorityqueue

import (
	"context"
	"errors"
)

var (
	errContention  = errors.New("queue contention")
	errBadPriority = errors.New("bad priority")

	wakey = struct{}{}
)

type PriorityQueue struct {
	queues []chan interface{}
	wakeup chan struct{}
}

func New(n int, l int) *PriorityQueue {
	var queues = make([]chan interface{}, n)
	for i := range queues {
		queues[i] = make(chan interface{}, l)
	}
	return &PriorityQueue{
		queues: queues,
		wakeup: make(chan struct{}, 1),
	}
}

func (pq *PriorityQueue) Run(ctx context.Context, f func(interface{})) {
	top := len(pq.queues) - 1
	p := top
READ:
	for {
		q := pq.queues[p]
		select {
		case <-ctx.Done():
			return
		case x := <-q:
			f(x)
			p = top
		default:
			if p > 0 {
				p--
				continue READ
			}
			p = top
			select {
			case <-ctx.Done():
				return
			case <-pq.wakeup:
			}
		}
	}
}

func (pq *PriorityQueue) Push(ctx context.Context, x interface{}, p int) error {
	if p < 0 || p >= len(pq.queues) {
		return errBadPriority
	}
	if ctx == nil {
		select {
		case pq.queues[p] <- x:
		default:
			return errContention
		}
	} else {
		select {
		case pq.queues[p] <- x:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case pq.wakeup <- wakey:
	default:
	}
	return nil
}
