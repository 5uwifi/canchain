package jsre

import (
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/internal/jsre/deps"
	"github.com/robertkrimen/otto"
)

var (
	BigNumber_JS = deps.MustAsset("bignumber.js")
	Web3_JS      = deps.MustAsset("web3.js")
)

/*
JSRE is a generic JS runtime environment embedding the otto JS interpreter.
It provides some helper functions to
- load code from files
- run code snippets
- require libraries
- bind native go objects
*/
type JSRE struct {
	assetPath     string
	output        io.Writer
	evalQueue     chan *evalReq
	stopEventLoop chan bool
	closed        chan struct{}
}

type jsTimer struct {
	timer    *time.Timer
	duration time.Duration
	interval bool
	call     otto.FunctionCall
}

type evalReq struct {
	fn   func(vm *otto.Otto)
	done chan bool
}

func New(assetPath string, output io.Writer) *JSRE {
	re := &JSRE{
		assetPath:     assetPath,
		output:        output,
		closed:        make(chan struct{}),
		evalQueue:     make(chan *evalReq),
		stopEventLoop: make(chan bool),
	}
	go re.runEventLoop()
	re.Set("loadScript", re.loadScript)
	re.Set("inspect", re.prettyPrintJS)
	return re
}

func randomSource() *rand.Rand {
	bytes := make([]byte, 8)
	seed := time.Now().UnixNano()
	if _, err := crand.Read(bytes); err == nil {
		seed = int64(binary.LittleEndian.Uint64(bytes))
	}

	src := rand.NewSource(seed)
	return rand.New(src)
}


func (re *JSRE) runEventLoop() {
	defer close(re.closed)

	vm := otto.New()
	r := randomSource()
	vm.SetRandomSource(r.Float64)

	registry := map[*jsTimer]*jsTimer{}
	ready := make(chan *jsTimer)

	newTimer := func(call otto.FunctionCall, interval bool) (*jsTimer, otto.Value) {
		delay, _ := call.Argument(1).ToInteger()
		if 0 >= delay {
			delay = 1
		}
		timer := &jsTimer{
			duration: time.Duration(delay) * time.Millisecond,
			call:     call,
			interval: interval,
		}
		registry[timer] = timer

		timer.timer = time.AfterFunc(timer.duration, func() {
			ready <- timer
		})

		value, err := call.Otto.ToValue(timer)
		if err != nil {
			panic(err)
		}
		return timer, value
	}

	setTimeout := func(call otto.FunctionCall) otto.Value {
		_, value := newTimer(call, false)
		return value
	}

	setInterval := func(call otto.FunctionCall) otto.Value {
		_, value := newTimer(call, true)
		return value
	}

	clearTimeout := func(call otto.FunctionCall) otto.Value {
		timer, _ := call.Argument(0).Export()
		if timer, ok := timer.(*jsTimer); ok {
			timer.timer.Stop()
			delete(registry, timer)
		}
		return otto.UndefinedValue()
	}
	vm.Set("_setTimeout", setTimeout)
	vm.Set("_setInterval", setInterval)
	vm.Run(`var setTimeout = function(args) {
		if (arguments.length < 1) {
			throw TypeError("Failed to execute 'setTimeout': 1 argument required, but only 0 present.");
		}
		return _setTimeout.apply(this, arguments);
	}`)
	vm.Run(`var setInterval = function(args) {
		if (arguments.length < 1) {
			throw TypeError("Failed to execute 'setInterval': 1 argument required, but only 0 present.");
		}
		return _setInterval.apply(this, arguments);
	}`)
	vm.Set("clearTimeout", clearTimeout)
	vm.Set("clearInterval", clearTimeout)

	var waitForCallbacks bool

loop:
	for {
		select {
		case timer := <-ready:
			// execute callback, remove/reschedule the timer
			var arguments []interface{}
			if len(timer.call.ArgumentList) > 2 {
				tmp := timer.call.ArgumentList[2:]
				arguments = make([]interface{}, 2+len(tmp))
				for i, value := range tmp {
					arguments[i+2] = value
				}
			} else {
				arguments = make([]interface{}, 1)
			}
			arguments[0] = timer.call.ArgumentList[0]
			_, err := vm.Call(`Function.call.call`, nil, arguments...)
			if err != nil {
				fmt.Println("js error:", err, arguments)
			}

			_, inreg := registry[timer] // when clearInterval is called from within the callback don't reset it
			if timer.interval && inreg {
				timer.timer.Reset(timer.duration)
			} else {
				delete(registry, timer)
				if waitForCallbacks && (len(registry) == 0) {
					break loop
				}
			}
		case req := <-re.evalQueue:
			// run the code, send the result back
			req.fn(vm)
			close(req.done)
			if waitForCallbacks && (len(registry) == 0) {
				break loop
			}
		case waitForCallbacks = <-re.stopEventLoop:
			if !waitForCallbacks || (len(registry) == 0) {
				break loop
			}
		}
	}

	for _, timer := range registry {
		timer.timer.Stop()
		delete(registry, timer)
	}
}

func (re *JSRE) Do(fn func(*otto.Otto)) {
	done := make(chan bool)
	req := &evalReq{fn, done}
	re.evalQueue <- req
	<-done
}

func (re *JSRE) Stop(waitForCallbacks bool) {
	select {
	case <-re.closed:
	case re.stopEventLoop <- waitForCallbacks:
		<-re.closed
	}
}

func (re *JSRE) Exec(file string) error {
	code, err := ioutil.ReadFile(common.AbsolutePath(re.assetPath, file))
	if err != nil {
		return err
	}
	var script *otto.Script
	re.Do(func(vm *otto.Otto) {
		script, err = vm.Compile(file, code)
		if err != nil {
			return
		}
		_, err = vm.Run(script)
	})
	return err
}

func (re *JSRE) Bind(name string, v interface{}) error {
	return re.Set(name, v)
}

func (re *JSRE) Run(code string) (v otto.Value, err error) {
	re.Do(func(vm *otto.Otto) { v, err = vm.Run(code) })
	return v, err
}

func (re *JSRE) Get(ns string) (v otto.Value, err error) {
	re.Do(func(vm *otto.Otto) { v, err = vm.Get(ns) })
	return v, err
}

func (re *JSRE) Set(ns string, v interface{}) (err error) {
	re.Do(func(vm *otto.Otto) { err = vm.Set(ns, v) })
	return err
}

func (re *JSRE) loadScript(call otto.FunctionCall) otto.Value {
	file, err := call.Argument(0).ToString()
	if err != nil {
		return otto.FalseValue()
	}
	file = common.AbsolutePath(re.assetPath, file)
	source, err := ioutil.ReadFile(file)
	if err != nil {
		return otto.FalseValue()
	}
	if _, err := compileAndRun(call.Otto, file, source); err != nil {
		fmt.Println("err:", err)
		return otto.FalseValue()
	}
	return otto.TrueValue()
}

func (re *JSRE) Evaluate(code string, w io.Writer) error {
	var fail error

	re.Do(func(vm *otto.Otto) {
		val, err := vm.Run(code)
		if err != nil {
			prettyError(vm, err, w)
		} else {
			prettyPrint(vm, val, w)
		}
		fmt.Fprintln(w)
	})
	return fail
}

func (re *JSRE) Compile(filename string, src interface{}) (err error) {
	re.Do(func(vm *otto.Otto) { _, err = compileAndRun(vm, filename, src) })
	return err
}

func compileAndRun(vm *otto.Otto, filename string, src interface{}) (otto.Value, error) {
	script, err := vm.Compile(filename, src)
	if err != nil {
		return otto.Value{}, err
	}
	return vm.Run(script)
}
