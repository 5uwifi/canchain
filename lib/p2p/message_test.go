package p2p

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

func ExampleMsgPipe() {
	rw1, rw2 := MsgPipe()
	go func() {
		Send(rw1, 8, [][]byte{{0, 0}})
		Send(rw1, 5, [][]byte{{1, 1}})
		rw1.Close()
	}()

	for {
		msg, err := rw2.ReadMsg()
		if err != nil {
			break
		}
		var data [][]byte
		msg.Decode(&data)
		fmt.Printf("msg: %d, %x\n", msg.Code, data[0])
	}
}

func TestMsgPipeUnblockWrite(t *testing.T) {
loop:
	for i := 0; i < 100; i++ {
		rw1, rw2 := MsgPipe()
		done := make(chan struct{})
		go func() {
			if err := SendItems(rw1, 1); err == nil {
				t.Error("EncodeMsg returned nil error")
			} else if err != ErrPipeClosed {
				t.Errorf("EncodeMsg returned wrong error: got %v, want %v", err, ErrPipeClosed)
			}
			close(done)
		}()

		runtime.Gosched()

		rw2.Close()
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			t.Errorf("write didn't unblock")
			break loop
		}
	}
}

func TestMsgPipeConcurrentClose(t *testing.T) {
	rw1, _ := MsgPipe()
	for i := 0; i < 10; i++ {
		go rw1.Close()
	}
}

func TestEOFSignal(t *testing.T) {
	rb := make([]byte, 10)

	eof := make(chan struct{}, 1)
	sig := &eofSignal{new(bytes.Buffer), 0, eof}
	if n, err := sig.Read(rb); n != 0 || err != io.EOF {
		t.Errorf("Read returned unexpected values: (%v, %v)", n, err)
	}
	select {
	case <-eof:
	default:
		t.Error("EOF chan not signaled")
	}

	eof = make(chan struct{}, 1)
	sig = &eofSignal{bytes.NewBufferString("aaaaaaaa"), 4, eof}
	if n, err := sig.Read(rb); n != 4 || err != nil {
		t.Errorf("Read returned unexpected values: (%v, %v)", n, err)
	}
	select {
	case <-eof:
	default:
		t.Error("EOF chan not signaled")
	}

	eof = make(chan struct{}, 1)
	sig = &eofSignal{bytes.NewBufferString("aaaa"), 999, eof}
	if n, err := sig.Read(rb); n != 4 || err != nil {
		t.Errorf("Read returned unexpected values: (%v, %v)", n, err)
	}
	if n, err := sig.Read(rb); n != 0 || err != io.EOF {
		t.Errorf("Read returned unexpected values: (%v, %v)", n, err)
	}
	select {
	case <-eof:
	default:
		t.Error("EOF chan not signaled")
	}

	eof = make(chan struct{}, 1)
	sig = &eofSignal{bytes.NewBufferString("aaaaaaaaaaaaaaaaaaaaa"), 999, eof}
	if n, err := sig.Read(rb); n != 10 || err != nil {
		t.Errorf("Read returned unexpected values: (%v, %v)", n, err)
	}
	select {
	case <-eof:
		t.Error("unexpected EOF signal")
	default:
	}
}

func unhex(str string) []byte {
	r := strings.NewReplacer("\t", "", " ", "", "\n", "")
	b, err := hex.DecodeString(r.Replace(str))
	if err != nil {
		panic(fmt.Sprintf("invalid hex string: %q", str))
	}
	return b
}
