package metrics

import "sync/atomic"

type Gauge interface {
	Snapshot() Gauge
	Update(int64)
	Value() int64
}

func GetOrRegisterGauge(name string, r Registry) Gauge {
	if nil == r {
		r = DefaultRegistry
	}
	return r.GetOrRegister(name, NewGauge).(Gauge)
}

func NewGauge() Gauge {
	if !Enabled {
		return NilGauge{}
	}
	return &StandardGauge{0}
}

func NewRegisteredGauge(name string, r Registry) Gauge {
	c := NewGauge()
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

func NewFunctionalGauge(f func() int64) Gauge {
	if !Enabled {
		return NilGauge{}
	}
	return &FunctionalGauge{value: f}
}

func NewRegisteredFunctionalGauge(name string, r Registry, f func() int64) Gauge {
	c := NewFunctionalGauge(f)
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

type GaugeSnapshot int64

func (g GaugeSnapshot) Snapshot() Gauge { return g }

func (GaugeSnapshot) Update(int64) {
	panic("Update called on a GaugeSnapshot")
}

func (g GaugeSnapshot) Value() int64 { return int64(g) }

type NilGauge struct{}

func (NilGauge) Snapshot() Gauge { return NilGauge{} }

func (NilGauge) Update(v int64) {}

func (NilGauge) Value() int64 { return 0 }

type StandardGauge struct {
	value int64
}

func (g *StandardGauge) Snapshot() Gauge {
	return GaugeSnapshot(g.Value())
}

func (g *StandardGauge) Update(v int64) {
	atomic.StoreInt64(&g.value, v)
}

func (g *StandardGauge) Value() int64 {
	return atomic.LoadInt64(&g.value)
}

type FunctionalGauge struct {
	value func() int64
}

func (g FunctionalGauge) Value() int64 {
	return g.value()
}

func (g FunctionalGauge) Snapshot() Gauge { return GaugeSnapshot(g.Value()) }

func (FunctionalGauge) Update(int64) {
	panic("Update called on a FunctionalGauge")
}
