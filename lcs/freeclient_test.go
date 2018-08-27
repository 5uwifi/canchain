package lcs

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common/mclock"
)

func TestFreeClientPoolL10C100(t *testing.T) {
	testFreeClientPool(t, 10, 100)
}

func TestFreeClientPoolL40C200(t *testing.T) {
	testFreeClientPool(t, 40, 200)
}

func TestFreeClientPoolL100C300(t *testing.T) {
	testFreeClientPool(t, 100, 300)
}

const testFreeClientPoolTicks = 500000

func testFreeClientPool(t *testing.T, connLimit, clientCount int) {
	var (
		clock     mclock.Simulated
		db        = candb.NewMemDatabase()
		pool      = newFreeClientPool(db, connLimit, 10000, &clock)
		connected = make([]bool, clientCount)
		connTicks = make([]int, clientCount)
		disconnCh = make(chan int, clientCount)
	)
	peerId := func(i int) string {
		return fmt.Sprintf("test peer #%d", i)
	}
	disconnFn := func(i int) func() {
		return func() {
			disconnCh <- i
		}
	}

	for i := 0; i < connLimit; i++ {
		if pool.connect(peerId(i), disconnFn(i)) {
			connected[i] = true
		} else {
			t.Fatalf("Test peer #%d rejected", i)
		}
	}
	if pool.connect(peerId(connLimit), disconnFn(connLimit)) {
		connected[connLimit] = true
		t.Fatalf("Peer accepted over connected limit")
	}

	for tickCounter := 0; tickCounter < testFreeClientPoolTicks; tickCounter++ {
		clock.Run(1 * time.Second)

		i := rand.Intn(clientCount)
		if connected[i] {
			pool.disconnect(peerId(i))
			connected[i] = false
			connTicks[i] += tickCounter
		} else {
			if pool.connect(peerId(i), disconnFn(i)) {
				connected[i] = true
				connTicks[i] -= tickCounter
			}
		}
	pollDisconnects:
		for {
			select {
			case i := <-disconnCh:
				pool.disconnect(peerId(i))
				if connected[i] {
					connTicks[i] += tickCounter
					connected[i] = false
				}
			default:
				break pollDisconnects
			}
		}
	}

	expTicks := testFreeClientPoolTicks * connLimit / clientCount
	expMin := expTicks - expTicks/10
	expMax := expTicks + expTicks/10

	for i, c := range connected {
		if c {
			connTicks[i] += testFreeClientPoolTicks
		}
		if connTicks[i] < expMin || connTicks[i] > expMax {
			t.Errorf("Total connected time of test node #%d (%d) outside expected range (%d to %d)", i, connTicks[i], expMin, expMax)
		}
	}

	if !pool.connect("newPeer", func() {}) {
		t.Fatalf("Previously unknown peer rejected")
	}

	pool.stop()
	pool = newFreeClientPool(db, connLimit, 10000, &clock)

	for i := 0; i < clientCount; i++ {
		pool.connect(peerId(i), func() {})
	}
	if !pool.connect("newPeer2", func() {}) {
		t.Errorf("Previously unknown peer rejected after restarting pool")
	}
	pool.stop()
}
