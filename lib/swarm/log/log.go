package log

import (
	l "github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/metrics"
)

const (
	CallDepth = 1
)

func Warn(msg string, ctx ...interface{}) {
	metrics.GetOrRegisterCounter("warn", nil).Inc(1)
	l.Output(msg, l.LvlWarn, CallDepth, ctx...)
}

func Error(msg string, ctx ...interface{}) {
	metrics.GetOrRegisterCounter("error", nil).Inc(1)
	l.Output(msg, l.LvlError, CallDepth, ctx...)
}

func Crit(msg string, ctx ...interface{}) {
	metrics.GetOrRegisterCounter("crit", nil).Inc(1)
	l.Output(msg, l.LvlCrit, CallDepth, ctx...)
}

func Info(msg string, ctx ...interface{}) {
	metrics.GetOrRegisterCounter("info", nil).Inc(1)
	l.Output(msg, l.LvlInfo, CallDepth, ctx...)
}

func Debug(msg string, ctx ...interface{}) {
	metrics.GetOrRegisterCounter("debug", nil).Inc(1)
	l.Output(msg, l.LvlDebug, CallDepth, ctx...)
}

func Trace(msg string, ctx ...interface{}) {
	metrics.GetOrRegisterCounter("trace", nil).Inc(1)
	l.Output(msg, l.LvlTrace, CallDepth, ctx...)
}
