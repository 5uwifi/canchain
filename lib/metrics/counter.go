package metrics

import "sync/atomic"

type Counter interface {
	Clear()
	Count() int64
	Dec(int64)
	Inc(int64)
	Snapshot() Counter
}

func GetOrRegisterCounter(name string, r Registry) Counter {
	if nil == r {
		r = DefaultRegistry
	}
	return r.GetOrRegister(name, NewCounter).(Counter)
}

func NewCounter() Counter {
	if !Enabled {
		return NilCounter{}
	}
	return &StandardCounter{0}
}

func NewRegisteredCounter(name string, r Registry) Counter {
	c := NewCounter()
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

type CounterSnapshot int64

func (CounterSnapshot) Clear() {
	panic("Clear called on a CounterSnapshot")
}

func (c CounterSnapshot) Count() int64 { return int64(c) }

func (CounterSnapshot) Dec(int64) {
	panic("Dec called on a CounterSnapshot")
}

func (CounterSnapshot) Inc(int64) {
	panic("Inc called on a CounterSnapshot")
}

func (c CounterSnapshot) Snapshot() Counter { return c }

type NilCounter struct{}

func (NilCounter) Clear() {}

func (NilCounter) Count() int64 { return 0 }

func (NilCounter) Dec(i int64) {}

func (NilCounter) Inc(i int64) {}

func (NilCounter) Snapshot() Counter { return NilCounter{} }

type StandardCounter struct {
	count int64
}

func (c *StandardCounter) Clear() {
	atomic.StoreInt64(&c.count, 0)
}

func (c *StandardCounter) Count() int64 {
	return atomic.LoadInt64(&c.count)
}

func (c *StandardCounter) Dec(i int64) {
	atomic.AddInt64(&c.count, -i)
}

func (c *StandardCounter) Inc(i int64) {
	atomic.AddInt64(&c.count, i)
}

func (c *StandardCounter) Snapshot() Counter {
	return CounterSnapshot(c.Count())
}
