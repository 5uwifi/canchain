package metrics

import (
	"math"
	"sync"
	"sync/atomic"
)

type EWMA interface {
	Rate() float64
	Snapshot() EWMA
	Tick()
	Update(int64)
}

func NewEWMA(alpha float64) EWMA {
	if !Enabled {
		return NilEWMA{}
	}
	return &StandardEWMA{alpha: alpha}
}

func NewEWMA1() EWMA {
	return NewEWMA(1 - math.Exp(-5.0/60.0/1))
}

func NewEWMA5() EWMA {
	return NewEWMA(1 - math.Exp(-5.0/60.0/5))
}

func NewEWMA15() EWMA {
	return NewEWMA(1 - math.Exp(-5.0/60.0/15))
}

type EWMASnapshot float64

func (a EWMASnapshot) Rate() float64 { return float64(a) }

func (a EWMASnapshot) Snapshot() EWMA { return a }

func (EWMASnapshot) Tick() {
	panic("Tick called on an EWMASnapshot")
}

func (EWMASnapshot) Update(int64) {
	panic("Update called on an EWMASnapshot")
}

type NilEWMA struct{}

func (NilEWMA) Rate() float64 { return 0.0 }

func (NilEWMA) Snapshot() EWMA { return NilEWMA{} }

func (NilEWMA) Tick() {}

func (NilEWMA) Update(n int64) {}

type StandardEWMA struct {
	uncounted int64
	alpha     float64
	rate      float64
	init      bool
	mutex     sync.Mutex
}

func (a *StandardEWMA) Rate() float64 {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	return a.rate * float64(1e9)
}

func (a *StandardEWMA) Snapshot() EWMA {
	return EWMASnapshot(a.Rate())
}

func (a *StandardEWMA) Tick() {
	count := atomic.LoadInt64(&a.uncounted)
	atomic.AddInt64(&a.uncounted, -count)
	instantRate := float64(count) / float64(5e9)
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if a.init {
		a.rate += a.alpha * (instantRate - a.rate)
	} else {
		a.init = true
		a.rate = instantRate
	}
}

func (a *StandardEWMA) Update(n int64) {
	atomic.AddInt64(&a.uncounted, n)
}
