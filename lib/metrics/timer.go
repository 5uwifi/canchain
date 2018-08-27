package metrics

import (
	"sync"
	"time"
)

type Timer interface {
	Count() int64
	Max() int64
	Mean() float64
	Min() int64
	Percentile(float64) float64
	Percentiles([]float64) []float64
	Rate1() float64
	Rate5() float64
	Rate15() float64
	RateMean() float64
	Snapshot() Timer
	StdDev() float64
	Stop()
	Sum() int64
	Time(func())
	Update(time.Duration)
	UpdateSince(time.Time)
	Variance() float64
}

func GetOrRegisterTimer(name string, r Registry) Timer {
	if nil == r {
		r = DefaultRegistry
	}
	return r.GetOrRegister(name, NewTimer).(Timer)
}

func NewCustomTimer(h Histogram, m Meter) Timer {
	if !Enabled {
		return NilTimer{}
	}
	return &StandardTimer{
		histogram: h,
		meter:     m,
	}
}

func NewRegisteredTimer(name string, r Registry) Timer {
	c := NewTimer()
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

func NewTimer() Timer {
	if !Enabled {
		return NilTimer{}
	}
	return &StandardTimer{
		histogram: NewHistogram(NewExpDecaySample(1028, 0.015)),
		meter:     NewMeter(),
	}
}

type NilTimer struct {
	h Histogram
	m Meter
}

func (NilTimer) Count() int64 { return 0 }

func (NilTimer) Max() int64 { return 0 }

func (NilTimer) Mean() float64 { return 0.0 }

func (NilTimer) Min() int64 { return 0 }

func (NilTimer) Percentile(p float64) float64 { return 0.0 }

func (NilTimer) Percentiles(ps []float64) []float64 {
	return make([]float64, len(ps))
}

func (NilTimer) Rate1() float64 { return 0.0 }

func (NilTimer) Rate5() float64 { return 0.0 }

func (NilTimer) Rate15() float64 { return 0.0 }

func (NilTimer) RateMean() float64 { return 0.0 }

func (NilTimer) Snapshot() Timer { return NilTimer{} }

func (NilTimer) StdDev() float64 { return 0.0 }

func (NilTimer) Stop() {}

func (NilTimer) Sum() int64 { return 0 }

func (NilTimer) Time(func()) {}

func (NilTimer) Update(time.Duration) {}

func (NilTimer) UpdateSince(time.Time) {}

func (NilTimer) Variance() float64 { return 0.0 }

type StandardTimer struct {
	histogram Histogram
	meter     Meter
	mutex     sync.Mutex
}

func (t *StandardTimer) Count() int64 {
	return t.histogram.Count()
}

func (t *StandardTimer) Max() int64 {
	return t.histogram.Max()
}

func (t *StandardTimer) Mean() float64 {
	return t.histogram.Mean()
}

func (t *StandardTimer) Min() int64 {
	return t.histogram.Min()
}

func (t *StandardTimer) Percentile(p float64) float64 {
	return t.histogram.Percentile(p)
}

func (t *StandardTimer) Percentiles(ps []float64) []float64 {
	return t.histogram.Percentiles(ps)
}

func (t *StandardTimer) Rate1() float64 {
	return t.meter.Rate1()
}

func (t *StandardTimer) Rate5() float64 {
	return t.meter.Rate5()
}

func (t *StandardTimer) Rate15() float64 {
	return t.meter.Rate15()
}

func (t *StandardTimer) RateMean() float64 {
	return t.meter.RateMean()
}

func (t *StandardTimer) Snapshot() Timer {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	return &TimerSnapshot{
		histogram: t.histogram.Snapshot().(*HistogramSnapshot),
		meter:     t.meter.Snapshot().(*MeterSnapshot),
	}
}

func (t *StandardTimer) StdDev() float64 {
	return t.histogram.StdDev()
}

func (t *StandardTimer) Stop() {
	t.meter.Stop()
}

func (t *StandardTimer) Sum() int64 {
	return t.histogram.Sum()
}

func (t *StandardTimer) Time(f func()) {
	ts := time.Now()
	f()
	t.Update(time.Since(ts))
}

func (t *StandardTimer) Update(d time.Duration) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.histogram.Update(int64(d))
	t.meter.Mark(1)
}

func (t *StandardTimer) UpdateSince(ts time.Time) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.histogram.Update(int64(time.Since(ts)))
	t.meter.Mark(1)
}

func (t *StandardTimer) Variance() float64 {
	return t.histogram.Variance()
}

type TimerSnapshot struct {
	histogram *HistogramSnapshot
	meter     *MeterSnapshot
}

func (t *TimerSnapshot) Count() int64 { return t.histogram.Count() }

func (t *TimerSnapshot) Max() int64 { return t.histogram.Max() }

func (t *TimerSnapshot) Mean() float64 { return t.histogram.Mean() }

func (t *TimerSnapshot) Min() int64 { return t.histogram.Min() }

func (t *TimerSnapshot) Percentile(p float64) float64 {
	return t.histogram.Percentile(p)
}

func (t *TimerSnapshot) Percentiles(ps []float64) []float64 {
	return t.histogram.Percentiles(ps)
}

func (t *TimerSnapshot) Rate1() float64 { return t.meter.Rate1() }

func (t *TimerSnapshot) Rate5() float64 { return t.meter.Rate5() }

func (t *TimerSnapshot) Rate15() float64 { return t.meter.Rate15() }

func (t *TimerSnapshot) RateMean() float64 { return t.meter.RateMean() }

func (t *TimerSnapshot) Snapshot() Timer { return t }

func (t *TimerSnapshot) StdDev() float64 { return t.histogram.StdDev() }

func (t *TimerSnapshot) Stop() {}

func (t *TimerSnapshot) Sum() int64 { return t.histogram.Sum() }

func (*TimerSnapshot) Time(func()) {
	panic("Time called on a TimerSnapshot")
}

func (*TimerSnapshot) Update(time.Duration) {
	panic("Update called on a TimerSnapshot")
}

func (*TimerSnapshot) UpdateSince(time.Time) {
	panic("UpdateSince called on a TimerSnapshot")
}

func (t *TimerSnapshot) Variance() float64 { return t.histogram.Variance() }
