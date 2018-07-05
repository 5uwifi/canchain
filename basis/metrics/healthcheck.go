package metrics

type Healthcheck interface {
	Check()
	Error() error
	Healthy()
	Unhealthy(error)
}

func NewHealthcheck(f func(Healthcheck)) Healthcheck {
	if !Enabled {
		return NilHealthcheck{}
	}
	return &StandardHealthcheck{nil, f}
}

type NilHealthcheck struct{}

func (NilHealthcheck) Check() {}

func (NilHealthcheck) Error() error { return nil }

func (NilHealthcheck) Healthy() {}

func (NilHealthcheck) Unhealthy(error) {}

type StandardHealthcheck struct {
	err error
	f   func(Healthcheck)
}

func (h *StandardHealthcheck) Check() {
	h.f(h)
}

func (h *StandardHealthcheck) Error() error {
	return h.err
}

func (h *StandardHealthcheck) Healthy() {
	h.err = nil
}

func (h *StandardHealthcheck) Unhealthy(err error) {
	h.err = err
}
