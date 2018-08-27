package mclock

import (
	"time"

	"github.com/aristanetworks/goarista/monotime"
)

type AbsTime time.Duration

func Now() AbsTime {
	return AbsTime(monotime.Now())
}

func (t AbsTime) Add(d time.Duration) AbsTime {
	return t + AbsTime(d)
}

type Clock interface {
	Now() AbsTime
	Sleep(time.Duration)
	After(time.Duration) <-chan time.Time
}

type System struct{}

func (System) Now() AbsTime {
	return AbsTime(monotime.Now())
}

func (System) Sleep(d time.Duration) {
	time.Sleep(d)
}

func (System) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}
