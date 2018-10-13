package netutil

import (
	"time"

	"github.com/5uwifi/canchain/common/mclock"
)

type IPTracker struct {
	window          time.Duration
	contactWindow   time.Duration
	minStatements   int
	clock           mclock.Clock
	statements      map[string]ipStatement
	contact         map[string]mclock.AbsTime
	lastStatementGC mclock.AbsTime
	lastContactGC   mclock.AbsTime
}

type ipStatement struct {
	endpoint string
	time     mclock.AbsTime
}

func NewIPTracker(window, contactWindow time.Duration, minStatements int) *IPTracker {
	return &IPTracker{
		window:        window,
		contactWindow: contactWindow,
		statements:    make(map[string]ipStatement),
		minStatements: minStatements,
		contact:       make(map[string]mclock.AbsTime),
		clock:         mclock.System{},
	}
}

func (it *IPTracker) PredictFullConeNAT() bool {
	now := it.clock.Now()
	it.gcContact(now)
	it.gcStatements(now)
	for host, st := range it.statements {
		if c, ok := it.contact[host]; !ok || c > st.time {
			return true
		}
	}
	return false
}

func (it *IPTracker) PredictEndpoint() string {
	it.gcStatements(it.clock.Now())

	counts := make(map[string]int)
	maxcount, max := 0, ""
	for _, s := range it.statements {
		c := counts[s.endpoint] + 1
		counts[s.endpoint] = c
		if c > maxcount && c >= it.minStatements {
			maxcount, max = c, s.endpoint
		}
	}
	return max
}

func (it *IPTracker) AddStatement(host, endpoint string) {
	now := it.clock.Now()
	it.statements[host] = ipStatement{endpoint, now}
	if time.Duration(now-it.lastStatementGC) >= it.window {
		it.gcStatements(now)
	}
}

func (it *IPTracker) AddContact(host string) {
	now := it.clock.Now()
	it.contact[host] = now
	if time.Duration(now-it.lastContactGC) >= it.contactWindow {
		it.gcContact(now)
	}
}

func (it *IPTracker) gcStatements(now mclock.AbsTime) {
	it.lastStatementGC = now
	cutoff := now.Add(-it.window)
	for host, s := range it.statements {
		if s.time < cutoff {
			delete(it.statements, host)
		}
	}
}

func (it *IPTracker) gcContact(now mclock.AbsTime) {
	it.lastContactGC = now
	cutoff := now.Add(-it.contactWindow)
	for host, ct := range it.contact {
		if ct < cutoff {
			delete(it.contact, host)
		}
	}
}
