package metrics

type Histogram interface {
	Clear()
	Count() int64
	Max() int64
	Mean() float64
	Min() int64
	Percentile(float64) float64
	Percentiles([]float64) []float64
	Sample() Sample
	Snapshot() Histogram
	StdDev() float64
	Sum() int64
	Update(int64)
	Variance() float64
}

func GetOrRegisterHistogram(name string, r Registry, s Sample) Histogram {
	if nil == r {
		r = DefaultRegistry
	}
	return r.GetOrRegister(name, func() Histogram { return NewHistogram(s) }).(Histogram)
}

func NewHistogram(s Sample) Histogram {
	if !Enabled {
		return NilHistogram{}
	}
	return &StandardHistogram{sample: s}
}

func NewRegisteredHistogram(name string, r Registry, s Sample) Histogram {
	c := NewHistogram(s)
	if nil == r {
		r = DefaultRegistry
	}
	r.Register(name, c)
	return c
}

type HistogramSnapshot struct {
	sample *SampleSnapshot
}

func (*HistogramSnapshot) Clear() {
	panic("Clear called on a HistogramSnapshot")
}

func (h *HistogramSnapshot) Count() int64 { return h.sample.Count() }

func (h *HistogramSnapshot) Max() int64 { return h.sample.Max() }

func (h *HistogramSnapshot) Mean() float64 { return h.sample.Mean() }

func (h *HistogramSnapshot) Min() int64 { return h.sample.Min() }

func (h *HistogramSnapshot) Percentile(p float64) float64 {
	return h.sample.Percentile(p)
}

func (h *HistogramSnapshot) Percentiles(ps []float64) []float64 {
	return h.sample.Percentiles(ps)
}

func (h *HistogramSnapshot) Sample() Sample { return h.sample }

func (h *HistogramSnapshot) Snapshot() Histogram { return h }

func (h *HistogramSnapshot) StdDev() float64 { return h.sample.StdDev() }

func (h *HistogramSnapshot) Sum() int64 { return h.sample.Sum() }

func (*HistogramSnapshot) Update(int64) {
	panic("Update called on a HistogramSnapshot")
}

func (h *HistogramSnapshot) Variance() float64 { return h.sample.Variance() }

type NilHistogram struct{}

func (NilHistogram) Clear() {}

func (NilHistogram) Count() int64 { return 0 }

func (NilHistogram) Max() int64 { return 0 }

func (NilHistogram) Mean() float64 { return 0.0 }

func (NilHistogram) Min() int64 { return 0 }

func (NilHistogram) Percentile(p float64) float64 { return 0.0 }

func (NilHistogram) Percentiles(ps []float64) []float64 {
	return make([]float64, len(ps))
}

func (NilHistogram) Sample() Sample { return NilSample{} }

func (NilHistogram) Snapshot() Histogram { return NilHistogram{} }

func (NilHistogram) StdDev() float64 { return 0.0 }

func (NilHistogram) Sum() int64 { return 0 }

func (NilHistogram) Update(v int64) {}

func (NilHistogram) Variance() float64 { return 0.0 }

type StandardHistogram struct {
	sample Sample
}

func (h *StandardHistogram) Clear() { h.sample.Clear() }

func (h *StandardHistogram) Count() int64 { return h.sample.Count() }

func (h *StandardHistogram) Max() int64 { return h.sample.Max() }

func (h *StandardHistogram) Mean() float64 { return h.sample.Mean() }

func (h *StandardHistogram) Min() int64 { return h.sample.Min() }

func (h *StandardHistogram) Percentile(p float64) float64 {
	return h.sample.Percentile(p)
}

func (h *StandardHistogram) Percentiles(ps []float64) []float64 {
	return h.sample.Percentiles(ps)
}

func (h *StandardHistogram) Sample() Sample { return h.sample }

func (h *StandardHistogram) Snapshot() Histogram {
	return &HistogramSnapshot{sample: h.sample.Snapshot().(*SampleSnapshot)}
}

func (h *StandardHistogram) StdDev() float64 { return h.sample.StdDev() }

func (h *StandardHistogram) Sum() int64 { return h.sample.Sum() }

func (h *StandardHistogram) Update(v int64) { h.sample.Update(v) }

func (h *StandardHistogram) Variance() float64 { return h.sample.Variance() }
