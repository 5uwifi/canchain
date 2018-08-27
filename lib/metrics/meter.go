package metrics

import (
	"sync"
	"time"
)

type Meter interface {
	Count() int64
	Mark(int64)
	Rate1() float64
	Rate5() float64
	Rate15() float64
	RateMean() float64
	Snapshot() Meter
	Stop()
}

func GetOrRegisterMeter(name string, r Registry) Meter {
	if nil == r {
		r = DefaultRegistry
	}
	return r.GetOrRegister(name, NewMeter).(Meter)
}

func NewMeter() Meter {
	if !Enabled {
		return NilMeter{}
	}
	m := newStandardMeter()
	arbiter.Lock()
	defer arbiter.Unlock()
	arbiter.meters[m] = struct{}{}
	if !arbiter.started {
		arbiter.started = true
		go arbiter.tick()
	}
	return m
}

func NewRegisteredMeter(name string, r Registry) Meter {
	c := NewMeter()
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

type MeterSnapshot struct {
	count                          int64
	rate1, rate5, rate15, rateMean float64
}

func (m *MeterSnapshot) Count() int64 { return m.count }

func (*MeterSnapshot) Mark(n int64) {
	panic("Mark called on a MeterSnapshot")
}

func (m *MeterSnapshot) Rate1() float64 { return m.rate1 }

func (m *MeterSnapshot) Rate5() float64 { return m.rate5 }

func (m *MeterSnapshot) Rate15() float64 { return m.rate15 }

func (m *MeterSnapshot) RateMean() float64 { return m.rateMean }

func (m *MeterSnapshot) Snapshot() Meter { return m }

func (m *MeterSnapshot) Stop() {}

type NilMeter struct{}

func (NilMeter) Count() int64 { return 0 }

func (NilMeter) Mark(n int64) {}

func (NilMeter) Rate1() float64 { return 0.0 }

func (NilMeter) Rate5() float64 { return 0.0 }

func (NilMeter) Rate15() float64 { return 0.0 }

func (NilMeter) RateMean() float64 { return 0.0 }

func (NilMeter) Snapshot() Meter { return NilMeter{} }

func (NilMeter) Stop() {}

type StandardMeter struct {
	lock        sync.RWMutex
	snapshot    *MeterSnapshot
	a1, a5, a15 EWMA
	startTime   time.Time
	stopped     bool
}

func newStandardMeter() *StandardMeter {
	return &StandardMeter{
		snapshot:  &MeterSnapshot{},
		a1:        NewEWMA1(),
		a5:        NewEWMA5(),
		a15:       NewEWMA15(),
		startTime: time.Now(),
	}
}

func (m *StandardMeter) Stop() {
	m.lock.Lock()
	stopped := m.stopped
	m.stopped = true
	m.lock.Unlock()
	if !stopped {
		arbiter.Lock()
		delete(arbiter.meters, m)
		arbiter.Unlock()
	}
}

func (m *StandardMeter) Count() int64 {
	m.lock.RLock()
	count := m.snapshot.count
	m.lock.RUnlock()
	return count
}

func (m *StandardMeter) Mark(n int64) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.stopped {
		return
	}
	m.snapshot.count += n
	m.a1.Update(n)
	m.a5.Update(n)
	m.a15.Update(n)
	m.updateSnapshot()
}

func (m *StandardMeter) Rate1() float64 {
	m.lock.RLock()
	rate1 := m.snapshot.rate1
	m.lock.RUnlock()
	return rate1
}

func (m *StandardMeter) Rate5() float64 {
	m.lock.RLock()
	rate5 := m.snapshot.rate5
	m.lock.RUnlock()
	return rate5
}

func (m *StandardMeter) Rate15() float64 {
	m.lock.RLock()
	rate15 := m.snapshot.rate15
	m.lock.RUnlock()
	return rate15
}

func (m *StandardMeter) RateMean() float64 {
	m.lock.RLock()
	rateMean := m.snapshot.rateMean
	m.lock.RUnlock()
	return rateMean
}

func (m *StandardMeter) Snapshot() Meter {
	m.lock.RLock()
	snapshot := *m.snapshot
	m.lock.RUnlock()
	return &snapshot
}

func (m *StandardMeter) updateSnapshot() {
	snapshot := m.snapshot
	snapshot.rate1 = m.a1.Rate()
	snapshot.rate5 = m.a5.Rate()
	snapshot.rate15 = m.a15.Rate()
	snapshot.rateMean = float64(snapshot.count) / time.Since(m.startTime).Seconds()
}

func (m *StandardMeter) tick() {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.a1.Tick()
	m.a5.Tick()
	m.a15.Tick()
	m.updateSnapshot()
}

type meterArbiter struct {
	sync.RWMutex
	started bool
	meters  map[*StandardMeter]struct{}
	ticker  *time.Ticker
}

var arbiter = meterArbiter{ticker: time.NewTicker(5e9), meters: make(map[*StandardMeter]struct{})}

func (ma *meterArbiter) tick() {
	for range ma.ticker.C {
		ma.tickMeters()
	}
}

func (ma *meterArbiter) tickMeters() {
	ma.RLock()
	defer ma.RUnlock()
	for meter := range ma.meters {
		meter.tick()
	}
}
