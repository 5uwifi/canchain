package metrics

import (
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

const rescaleThreshold = time.Hour

type Sample interface {
	Clear()
	Count() int64
	Max() int64
	Mean() float64
	Min() int64
	Percentile(float64) float64
	Percentiles([]float64) []float64
	Size() int
	Snapshot() Sample
	StdDev() float64
	Sum() int64
	Update(int64)
	Values() []int64
	Variance() float64
}

type ExpDecaySample struct {
	alpha         float64
	count         int64
	mutex         sync.Mutex
	reservoirSize int
	t0, t1        time.Time
	values        *expDecaySampleHeap
}

func NewExpDecaySample(reservoirSize int, alpha float64) Sample {
	if !Enabled {
		return NilSample{}
	}
	s := &ExpDecaySample{
		alpha:         alpha,
		reservoirSize: reservoirSize,
		t0:            time.Now(),
		values:        newExpDecaySampleHeap(reservoirSize),
	}
	s.t1 = s.t0.Add(rescaleThreshold)
	return s
}

func (s *ExpDecaySample) Clear() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.count = 0
	s.t0 = time.Now()
	s.t1 = s.t0.Add(rescaleThreshold)
	s.values.Clear()
}

func (s *ExpDecaySample) Count() int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.count
}

func (s *ExpDecaySample) Max() int64 {
	return SampleMax(s.Values())
}

func (s *ExpDecaySample) Mean() float64 {
	return SampleMean(s.Values())
}

func (s *ExpDecaySample) Min() int64 {
	return SampleMin(s.Values())
}

func (s *ExpDecaySample) Percentile(p float64) float64 {
	return SamplePercentile(s.Values(), p)
}

func (s *ExpDecaySample) Percentiles(ps []float64) []float64 {
	return SamplePercentiles(s.Values(), ps)
}

func (s *ExpDecaySample) Size() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.values.Size()
}

func (s *ExpDecaySample) Snapshot() Sample {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	vals := s.values.Values()
	values := make([]int64, len(vals))
	for i, v := range vals {
		values[i] = v.v
	}
	return &SampleSnapshot{
		count:  s.count,
		values: values,
	}
}

func (s *ExpDecaySample) StdDev() float64 {
	return SampleStdDev(s.Values())
}

func (s *ExpDecaySample) Sum() int64 {
	return SampleSum(s.Values())
}

func (s *ExpDecaySample) Update(v int64) {
	s.update(time.Now(), v)
}

func (s *ExpDecaySample) Values() []int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	vals := s.values.Values()
	values := make([]int64, len(vals))
	for i, v := range vals {
		values[i] = v.v
	}
	return values
}

func (s *ExpDecaySample) Variance() float64 {
	return SampleVariance(s.Values())
}

func (s *ExpDecaySample) update(t time.Time, v int64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.count++
	if s.values.Size() == s.reservoirSize {
		s.values.Pop()
	}
	s.values.Push(expDecaySample{
		k: math.Exp(t.Sub(s.t0).Seconds()*s.alpha) / rand.Float64(),
		v: v,
	})
	if t.After(s.t1) {
		values := s.values.Values()
		t0 := s.t0
		s.values.Clear()
		s.t0 = t
		s.t1 = s.t0.Add(rescaleThreshold)
		for _, v := range values {
			v.k = v.k * math.Exp(-s.alpha*s.t0.Sub(t0).Seconds())
			s.values.Push(v)
		}
	}
}

type NilSample struct{}

func (NilSample) Clear() {}

func (NilSample) Count() int64 { return 0 }

func (NilSample) Max() int64 { return 0 }

func (NilSample) Mean() float64 { return 0.0 }

func (NilSample) Min() int64 { return 0 }

func (NilSample) Percentile(p float64) float64 { return 0.0 }

func (NilSample) Percentiles(ps []float64) []float64 {
	return make([]float64, len(ps))
}

func (NilSample) Size() int { return 0 }

func (NilSample) Snapshot() Sample { return NilSample{} }

func (NilSample) StdDev() float64 { return 0.0 }

func (NilSample) Sum() int64 { return 0 }

func (NilSample) Update(v int64) {}

func (NilSample) Values() []int64 { return []int64{} }

func (NilSample) Variance() float64 { return 0.0 }

func SampleMax(values []int64) int64 {
	if 0 == len(values) {
		return 0
	}
	var max int64 = math.MinInt64
	for _, v := range values {
		if max < v {
			max = v
		}
	}
	return max
}

func SampleMean(values []int64) float64 {
	if 0 == len(values) {
		return 0.0
	}
	return float64(SampleSum(values)) / float64(len(values))
}

func SampleMin(values []int64) int64 {
	if 0 == len(values) {
		return 0
	}
	var min int64 = math.MaxInt64
	for _, v := range values {
		if min > v {
			min = v
		}
	}
	return min
}

func SamplePercentile(values int64Slice, p float64) float64 {
	return SamplePercentiles(values, []float64{p})[0]
}

func SamplePercentiles(values int64Slice, ps []float64) []float64 {
	scores := make([]float64, len(ps))
	size := len(values)
	if size > 0 {
		sort.Sort(values)
		for i, p := range ps {
			pos := p * float64(size+1)
			if pos < 1.0 {
				scores[i] = float64(values[0])
			} else if pos >= float64(size) {
				scores[i] = float64(values[size-1])
			} else {
				lower := float64(values[int(pos)-1])
				upper := float64(values[int(pos)])
				scores[i] = lower + (pos-math.Floor(pos))*(upper-lower)
			}
		}
	}
	return scores
}

type SampleSnapshot struct {
	count  int64
	values []int64
}

func NewSampleSnapshot(count int64, values []int64) *SampleSnapshot {
	return &SampleSnapshot{
		count:  count,
		values: values,
	}
}

func (*SampleSnapshot) Clear() {
	panic("Clear called on a SampleSnapshot")
}

func (s *SampleSnapshot) Count() int64 { return s.count }

func (s *SampleSnapshot) Max() int64 { return SampleMax(s.values) }

func (s *SampleSnapshot) Mean() float64 { return SampleMean(s.values) }

func (s *SampleSnapshot) Min() int64 { return SampleMin(s.values) }

func (s *SampleSnapshot) Percentile(p float64) float64 {
	return SamplePercentile(s.values, p)
}

func (s *SampleSnapshot) Percentiles(ps []float64) []float64 {
	return SamplePercentiles(s.values, ps)
}

func (s *SampleSnapshot) Size() int { return len(s.values) }

func (s *SampleSnapshot) Snapshot() Sample { return s }

func (s *SampleSnapshot) StdDev() float64 { return SampleStdDev(s.values) }

func (s *SampleSnapshot) Sum() int64 { return SampleSum(s.values) }

func (*SampleSnapshot) Update(int64) {
	panic("Update called on a SampleSnapshot")
}

func (s *SampleSnapshot) Values() []int64 {
	values := make([]int64, len(s.values))
	copy(values, s.values)
	return values
}

func (s *SampleSnapshot) Variance() float64 { return SampleVariance(s.values) }

func SampleStdDev(values []int64) float64 {
	return math.Sqrt(SampleVariance(values))
}

func SampleSum(values []int64) int64 {
	var sum int64
	for _, v := range values {
		sum += v
	}
	return sum
}

func SampleVariance(values []int64) float64 {
	if 0 == len(values) {
		return 0.0
	}
	m := SampleMean(values)
	var sum float64
	for _, v := range values {
		d := float64(v) - m
		sum += d * d
	}
	return sum / float64(len(values))
}

type UniformSample struct {
	count         int64
	mutex         sync.Mutex
	reservoirSize int
	values        []int64
}

func NewUniformSample(reservoirSize int) Sample {
	if !Enabled {
		return NilSample{}
	}
	return &UniformSample{
		reservoirSize: reservoirSize,
		values:        make([]int64, 0, reservoirSize),
	}
}

func (s *UniformSample) Clear() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.count = 0
	s.values = make([]int64, 0, s.reservoirSize)
}

func (s *UniformSample) Count() int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.count
}

func (s *UniformSample) Max() int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SampleMax(s.values)
}

func (s *UniformSample) Mean() float64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SampleMean(s.values)
}

func (s *UniformSample) Min() int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SampleMin(s.values)
}

func (s *UniformSample) Percentile(p float64) float64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SamplePercentile(s.values, p)
}

func (s *UniformSample) Percentiles(ps []float64) []float64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SamplePercentiles(s.values, ps)
}

func (s *UniformSample) Size() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.values)
}

func (s *UniformSample) Snapshot() Sample {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	values := make([]int64, len(s.values))
	copy(values, s.values)
	return &SampleSnapshot{
		count:  s.count,
		values: values,
	}
}

func (s *UniformSample) StdDev() float64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SampleStdDev(s.values)
}

func (s *UniformSample) Sum() int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SampleSum(s.values)
}

func (s *UniformSample) Update(v int64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.count++
	if len(s.values) < s.reservoirSize {
		s.values = append(s.values, v)
	} else {
		r := rand.Int63n(s.count)
		if r < int64(len(s.values)) {
			s.values[int(r)] = v
		}
	}
}

func (s *UniformSample) Values() []int64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	values := make([]int64, len(s.values))
	copy(values, s.values)
	return values
}

func (s *UniformSample) Variance() float64 {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return SampleVariance(s.values)
}

type expDecaySample struct {
	k float64
	v int64
}

func newExpDecaySampleHeap(reservoirSize int) *expDecaySampleHeap {
	return &expDecaySampleHeap{make([]expDecaySample, 0, reservoirSize)}
}

type expDecaySampleHeap struct {
	s []expDecaySample
}

func (h *expDecaySampleHeap) Clear() {
	h.s = h.s[:0]
}

func (h *expDecaySampleHeap) Push(s expDecaySample) {
	n := len(h.s)
	h.s = h.s[0 : n+1]
	h.s[n] = s
	h.up(n)
}

func (h *expDecaySampleHeap) Pop() expDecaySample {
	n := len(h.s) - 1
	h.s[0], h.s[n] = h.s[n], h.s[0]
	h.down(0, n)

	n = len(h.s)
	s := h.s[n-1]
	h.s = h.s[0 : n-1]
	return s
}

func (h *expDecaySampleHeap) Size() int {
	return len(h.s)
}

func (h *expDecaySampleHeap) Values() []expDecaySample {
	return h.s
}

func (h *expDecaySampleHeap) up(j int) {
	for {
		i := (j - 1) / 2
		if i == j || !(h.s[j].k < h.s[i].k) {
			break
		}
		h.s[i], h.s[j] = h.s[j], h.s[i]
		j = i
	}
}

func (h *expDecaySampleHeap) down(i, n int) {
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && !(h.s[j1].k < h.s[j2].k) {
			j = j2
		}
		if !(h.s[j].k < h.s[i].k) {
			break
		}
		h.s[i], h.s[j] = h.s[j], h.s[i]
		i = j
	}
}

type int64Slice []int64

func (p int64Slice) Len() int           { return len(p) }
func (p int64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p int64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
