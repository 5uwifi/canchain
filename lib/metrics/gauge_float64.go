package metrics

import "sync"

type GaugeFloat64 interface {
	Snapshot() GaugeFloat64
	Update(float64)
	Value() float64
}

func GetOrRegisterGaugeFloat64(name string, r Registry) GaugeFloat64 {
	if nil == r {
		r = DefaultRegistry
	}
	return r.GetOrRegister(name, NewGaugeFloat64()).(GaugeFloat64)
}

func NewGaugeFloat64() GaugeFloat64 {
	if !Enabled {
		return NilGaugeFloat64{}
	}
	return &StandardGaugeFloat64{
		value: 0.0,
	}
}

func NewRegisteredGaugeFloat64(name string, r Registry) GaugeFloat64 {
	c := NewGaugeFloat64()
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

func NewFunctionalGaugeFloat64(f func() float64) GaugeFloat64 {
	if !Enabled {
		return NilGaugeFloat64{}
	}
	return &FunctionalGaugeFloat64{value: f}
}

func NewRegisteredFunctionalGaugeFloat64(name string, r Registry, f func() float64) GaugeFloat64 {
	c := NewFunctionalGaugeFloat64(f)
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

type GaugeFloat64Snapshot float64

func (g GaugeFloat64Snapshot) Snapshot() GaugeFloat64 { return g }

func (GaugeFloat64Snapshot) Update(float64) {
	panic("Update called on a GaugeFloat64Snapshot")
}

func (g GaugeFloat64Snapshot) Value() float64 { return float64(g) }

type NilGaugeFloat64 struct{}

func (NilGaugeFloat64) Snapshot() GaugeFloat64 { return NilGaugeFloat64{} }

func (NilGaugeFloat64) Update(v float64) {}

func (NilGaugeFloat64) Value() float64 { return 0.0 }

type StandardGaugeFloat64 struct {
	mutex sync.Mutex
	value float64
}

func (g *StandardGaugeFloat64) Snapshot() GaugeFloat64 {
	return GaugeFloat64Snapshot(g.Value())
}

func (g *StandardGaugeFloat64) Update(v float64) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.value = v
}

func (g *StandardGaugeFloat64) Value() float64 {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	return g.value
}

type FunctionalGaugeFloat64 struct {
	value func() float64
}

func (g FunctionalGaugeFloat64) Value() float64 {
	return g.value()
}

func (g FunctionalGaugeFloat64) Snapshot() GaugeFloat64 { return GaugeFloat64Snapshot(g.Value()) }

func (FunctionalGaugeFloat64) Update(float64) {
	panic("Update called on a FunctionalGaugeFloat64")
}
