package metrics

import (
	"math"
	"sort"
	"sync"
	"time"
)

const InitialResettingTimerSliceCap = 10

type ResettingTimer interface {
	Values() []int64
	Snapshot() ResettingTimer
	Percentiles([]float64) []int64
	Mean() float64
	Time(func())
	Update(time.Duration)
	UpdateSince(time.Time)
}

func GetOrRegisterResettingTimer(name string, r Registry) ResettingTimer {
	if nil == r {
		r = DefaultRegistry
	}
	return r.GetOrRegister(name, NewResettingTimer).(ResettingTimer)
}

func NewRegisteredResettingTimer(name string, r Registry) ResettingTimer {
	c := NewResettingTimer()
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

func NewResettingTimer() ResettingTimer {
	if !Enabled {
		return NilResettingTimer{}
	}
	return &StandardResettingTimer{
		values: make([]int64, 0, InitialResettingTimerSliceCap),
	}
}

type NilResettingTimer struct {
}

func (NilResettingTimer) Values() []int64 { return nil }

func (NilResettingTimer) Snapshot() ResettingTimer {
	return &ResettingTimerSnapshot{
		values: []int64{},
	}
}

func (NilResettingTimer) Time(func()) {}

func (NilResettingTimer) Update(time.Duration) {}

func (NilResettingTimer) Percentiles([]float64) []int64 {
	panic("Percentiles called on a NilResettingTimer")
}

func (NilResettingTimer) Mean() float64 {
	panic("Mean called on a NilResettingTimer")
}

func (NilResettingTimer) UpdateSince(time.Time) {}

type StandardResettingTimer struct {
	values []int64
	mutex  sync.Mutex
}

func (t *StandardResettingTimer) Values() []int64 {
	return t.values
}

func (t *StandardResettingTimer) Snapshot() ResettingTimer {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	currentValues := t.values
	t.values = make([]int64, 0, InitialResettingTimerSliceCap)

	return &ResettingTimerSnapshot{
		values: currentValues,
	}
}

func (t *StandardResettingTimer) Percentiles([]float64) []int64 {
	panic("Percentiles called on a StandardResettingTimer")
}

func (t *StandardResettingTimer) Mean() float64 {
	panic("Mean called on a StandardResettingTimer")
}

func (t *StandardResettingTimer) Time(f func()) {
	ts := time.Now()
	f()
	t.Update(time.Since(ts))
}

func (t *StandardResettingTimer) Update(d time.Duration) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.values = append(t.values, int64(d))
}

func (t *StandardResettingTimer) UpdateSince(ts time.Time) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.values = append(t.values, int64(time.Since(ts)))
}

type ResettingTimerSnapshot struct {
	values              []int64
	mean                float64
	thresholdBoundaries []int64
	calculated          bool
}

func (t *ResettingTimerSnapshot) Snapshot() ResettingTimer { return t }

func (*ResettingTimerSnapshot) Time(func()) {
	panic("Time called on a ResettingTimerSnapshot")
}

func (*ResettingTimerSnapshot) Update(time.Duration) {
	panic("Update called on a ResettingTimerSnapshot")
}

func (*ResettingTimerSnapshot) UpdateSince(time.Time) {
	panic("UpdateSince called on a ResettingTimerSnapshot")
}

func (t *ResettingTimerSnapshot) Values() []int64 {
	return t.values
}

func (t *ResettingTimerSnapshot) Percentiles(percentiles []float64) []int64 {
	t.calc(percentiles)

	return t.thresholdBoundaries
}

func (t *ResettingTimerSnapshot) Mean() float64 {
	if !t.calculated {
		t.calc([]float64{})
	}

	return t.mean
}

func (t *ResettingTimerSnapshot) calc(percentiles []float64) {
	sort.Sort(Int64Slice(t.values))

	count := len(t.values)
	if count > 0 {
		min := t.values[0]
		max := t.values[count-1]

		cumulativeValues := make([]int64, count)
		cumulativeValues[0] = min
		for i := 1; i < count; i++ {
			cumulativeValues[i] = t.values[i] + cumulativeValues[i-1]
		}

		t.thresholdBoundaries = make([]int64, len(percentiles))

		thresholdBoundary := max

		for i, pct := range percentiles {
			if count > 1 {
				var abs float64
				if pct >= 0 {
					abs = pct
				} else {
					abs = 100 + pct
				}
				indexOfPerc := int(math.Floor(((abs / 100.0) * float64(count)) + 0.5))
				if pct >= 0 && indexOfPerc > 0 {
					indexOfPerc -= 1
				}
				thresholdBoundary = t.values[indexOfPerc]
			}

			t.thresholdBoundaries[i] = thresholdBoundary
		}

		sum := cumulativeValues[count-1]
		t.mean = float64(sum) / float64(count)
	} else {
		t.thresholdBoundaries = make([]int64, len(percentiles))
		t.mean = 0
	}

	t.calculated = true
}

type Int64Slice []int64

func (s Int64Slice) Len() int           { return len(s) }
func (s Int64Slice) Less(i, j int) bool { return s[i] < s[j] }
func (s Int64Slice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
