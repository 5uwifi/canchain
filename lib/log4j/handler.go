package log4j

import (
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sync"

	"io/ioutil"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-stack/stack"
)

type Handler interface {
	Log(r *Record) error
}

func FuncHandler(fn func(r *Record) error) Handler {
	return funcHandler(fn)
}

type funcHandler func(r *Record) error

func (h funcHandler) Log(r *Record) error {
	return h(r)
}

func StreamHandler(wr io.Writer, fmtr Format) Handler {
	h := FuncHandler(func(r *Record) error {
		_, err := wr.Write(fmtr.Format(r))
		return err
	})
	return LazyHandler(SyncHandler(h))
}

func SyncHandler(h Handler) Handler {
	var mu sync.Mutex
	return FuncHandler(func(r *Record) error {
		defer mu.Unlock()
		mu.Lock()
		return h.Log(r)
	})
}

func FileHandler(path string, fmtr Format) (Handler, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	return closingHandler{f, StreamHandler(f, fmtr)}, nil
}

type countingWriter struct {
	w     io.WriteCloser
	count uint
}

func (w *countingWriter) Write(p []byte) (n int, err error) {
	n, err = w.w.Write(p)
	w.count += uint(n)
	return n, err
}

func (w *countingWriter) Close() error {
	return w.w.Close()
}

func prepFile(path string) (*countingWriter, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Seek(-1, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 1)
	var cut int64
	for {
		if _, err := f.Read(buf); err != nil {
			return nil, err
		}
		if buf[0] == '\n' {
			break
		}
		if _, err = f.Seek(-2, io.SeekCurrent); err != nil {
			return nil, err
		}
		cut++
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	ns := fi.Size() - cut
	if err = f.Truncate(ns); err != nil {
		return nil, err
	}
	return &countingWriter{w: f, count: uint(ns)}, nil
}

func RotatingFileHandler(path string, limit uint, formatter Format) (Handler, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	files, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`\.log$`)
	last := len(files) - 1
	for last >= 0 && (!files[last].Mode().IsRegular() || !re.MatchString(files[last].Name())) {
		last--
	}
	var counter *countingWriter
	if last >= 0 && files[last].Size() < int64(limit) {
		if counter, err = prepFile(filepath.Join(path, files[last].Name())); err != nil {
			return nil, err
		}
	}
	if counter == nil {
		counter = new(countingWriter)
	}
	h := StreamHandler(counter, formatter)

	return FuncHandler(func(r *Record) error {
		if counter.count > limit {
			counter.Close()
			counter.w = nil
		}
		if counter.w == nil {
			f, err := os.OpenFile(
				filepath.Join(path, fmt.Sprintf("%s.log", strings.Replace(r.Time.Format("060102150405.00"), ".", "", 1))),
				os.O_CREATE|os.O_APPEND|os.O_WRONLY,
				0600,
			)
			if err != nil {
				return err
			}
			counter.w = f
			counter.count = 0
		}
		return h.Log(r)
	}), nil
}

func NetHandler(network, addr string, fmtr Format) (Handler, error) {
	conn, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}

	return closingHandler{conn, StreamHandler(conn, fmtr)}, nil
}

type closingHandler struct {
	io.WriteCloser
	Handler
}

func (h *closingHandler) Close() error {
	return h.WriteCloser.Close()
}

func CallerFileHandler(h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		r.Ctx = append(r.Ctx, "caller", fmt.Sprint(r.Call))
		return h.Log(r)
	})
}

func CallerFuncHandler(h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		r.Ctx = append(r.Ctx, "fn", formatCall("%+n", r.Call))
		return h.Log(r)
	})
}

func formatCall(format string, c stack.Call) string {
	return fmt.Sprintf(format, c)
}

func CallerStackHandler(format string, h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		s := stack.Trace().TrimBelow(r.Call).TrimRuntime()
		if len(s) > 0 {
			r.Ctx = append(r.Ctx, "stack", fmt.Sprintf(format, s))
		}
		return h.Log(r)
	})
}

func FilterHandler(fn func(r *Record) bool, h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		if fn(r) {
			return h.Log(r)
		}
		return nil
	})
}

func MatchFilterHandler(key string, value interface{}, h Handler) Handler {
	return FilterHandler(func(r *Record) (pass bool) {
		switch key {
		case r.KeyNames.Lvl:
			return r.Lvl == value
		case r.KeyNames.Time:
			return r.Time == value
		case r.KeyNames.Msg:
			return r.Msg == value
		}

		for i := 0; i < len(r.Ctx); i += 2 {
			if r.Ctx[i] == key {
				return r.Ctx[i+1] == value
			}
		}
		return false
	}, h)
}

func LvlFilterHandler(maxLvl Lvl, h Handler) Handler {
	return FilterHandler(func(r *Record) (pass bool) {
		return r.Lvl <= maxLvl
	}, h)
}

func MultiHandler(hs ...Handler) Handler {
	return FuncHandler(func(r *Record) error {
		for _, h := range hs {
			h.Log(r)
		}
		return nil
	})
}

func FailoverHandler(hs ...Handler) Handler {
	return FuncHandler(func(r *Record) error {
		var err error
		for i, h := range hs {
			err = h.Log(r)
			if err == nil {
				return nil
			}
			r.Ctx = append(r.Ctx, fmt.Sprintf("failover_err_%d", i), err)
		}

		return err
	})
}

func ChannelHandler(recs chan<- *Record) Handler {
	return FuncHandler(func(r *Record) error {
		recs <- r
		return nil
	})
}

func BufferedHandler(bufSize int, h Handler) Handler {
	recs := make(chan *Record, bufSize)
	go func() {
		for m := range recs {
			_ = h.Log(m)
		}
	}()
	return ChannelHandler(recs)
}

func LazyHandler(h Handler) Handler {
	return FuncHandler(func(r *Record) error {
		hadErr := false
		for i := 1; i < len(r.Ctx); i += 2 {
			lz, ok := r.Ctx[i].(Lazy)
			if ok {
				v, err := evaluateLazy(lz)
				if err != nil {
					hadErr = true
					r.Ctx[i] = err
				} else {
					if cs, ok := v.(stack.CallStack); ok {
						v = cs.TrimBelow(r.Call).TrimRuntime()
					}
					r.Ctx[i] = v
				}
			}
		}

		if hadErr {
			r.Ctx = append(r.Ctx, errorKey, "bad lazy")
		}

		return h.Log(r)
	})
}

func evaluateLazy(lz Lazy) (interface{}, error) {
	t := reflect.TypeOf(lz.Fn)

	if t.Kind() != reflect.Func {
		return nil, fmt.Errorf("INVALID_LAZY, not func: %+v", lz.Fn)
	}

	if t.NumIn() > 0 {
		return nil, fmt.Errorf("INVALID_LAZY, func takes args: %+v", lz.Fn)
	}

	if t.NumOut() == 0 {
		return nil, fmt.Errorf("INVALID_LAZY, no func return val: %+v", lz.Fn)
	}

	value := reflect.ValueOf(lz.Fn)
	results := value.Call([]reflect.Value{})
	if len(results) == 1 {
		return results[0].Interface(), nil
	}
	values := make([]interface{}, len(results))
	for i, v := range results {
		values[i] = v.Interface()
	}
	return values, nil
}

func DiscardHandler() Handler {
	return FuncHandler(func(r *Record) error {
		return nil
	})
}

var Must muster

func must(h Handler, err error) Handler {
	if err != nil {
		panic(err)
	}
	return h
}

type muster struct{}

func (m muster) FileHandler(path string, fmtr Format) Handler {
	return must(FileHandler(path, fmtr))
}

func (m muster) NetHandler(network, addr string, fmtr Format) Handler {
	return must(NetHandler(network, addr, fmtr))
}
