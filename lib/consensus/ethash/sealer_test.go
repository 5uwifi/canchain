package ethash

import (
	"encoding/json"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
)

func TestRemoteNotify(t *testing.T) {
	sink := make(chan [3]string)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			blob, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("failed to read miner notification: %v", err)
			}
			var work [3]string
			if err := json.Unmarshal(blob, &work); err != nil {
				t.Fatalf("failed to unmarshal miner notification: %v", err)
			}
			sink <- work
		}),
	}
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to open notification server: %v", err)
	}
	defer listener.Close()

	go server.Serve(listener)

	ethash := NewTester([]string{"http://" + listener.Addr().String()})
	defer ethash.Close()

	header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(100)}
	block := types.NewBlockWithHeader(header)

	ethash.Seal(nil, block, nil)
	select {
	case work := <-sink:
		if want := ethash.SealHash(header).Hex(); work[0] != want {
			t.Errorf("work packet hash mismatch: have %s, want %s", work[0], want)
		}
		if want := common.BytesToHash(SeedHash(header.Number.Uint64())).Hex(); work[1] != want {
			t.Errorf("work packet seed mismatch: have %s, want %s", work[1], want)
		}
		target := new(big.Int).Div(new(big.Int).Lsh(big.NewInt(1), 256), header.Difficulty)
		if want := common.BytesToHash(target.Bytes()).Hex(); work[2] != want {
			t.Errorf("work packet target mismatch: have %s, want %s", work[2], want)
		}
	case <-time.After(time.Second):
		t.Fatalf("notification timed out")
	}
}

func TestRemoteMultiNotify(t *testing.T) {
	sink := make(chan [3]string, 64)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			blob, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("failed to read miner notification: %v", err)
			}
			var work [3]string
			if err := json.Unmarshal(blob, &work); err != nil {
				t.Fatalf("failed to unmarshal miner notification: %v", err)
			}
			sink <- work
		}),
	}
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to open notification server: %v", err)
	}
	defer listener.Close()

	go server.Serve(listener)

	ethash := NewTester([]string{"http://" + listener.Addr().String()})
	defer ethash.Close()

	for i := 0; i < cap(sink); i++ {
		header := &types.Header{Number: big.NewInt(int64(i)), Difficulty: big.NewInt(100)}
		block := types.NewBlockWithHeader(header)

		ethash.Seal(nil, block, nil)
	}
	for i := 0; i < cap(sink); i++ {
		select {
		case <-sink:
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("notification %d timed out", i)
		}
	}
}
