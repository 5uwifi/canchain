package ledger

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/5uwifi/canchain/ledger/downloader"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/p2p/discover"
)

// Tests that fast sync gets disabled as soon as a real block is successfully
// imported into the blockchain.
func TestFastSyncDisabling(t *testing.T) {
	// Create a pristine protocol manager, check that fast sync is left enabled
	pmEmpty, _ := newTestProtocolManagerMust(t, downloader.FastSync, 0, nil, nil)
	if atomic.LoadUint32(&pmEmpty.fastSync) == 0 {
		t.Fatalf("fast sync disabled on pristine blockchain")
	}
	// Create a full protocol manager, check that fast sync gets disabled
	pmFull, _ := newTestProtocolManagerMust(t, downloader.FastSync, 1024, nil, nil)
	if atomic.LoadUint32(&pmFull.fastSync) == 1 {
		t.Fatalf("fast sync not disabled on non-empty blockchain")
	}
	// Sync up the two peers
	io1, io2 := p2p.MsgPipe()

	go pmFull.handle(pmFull.newPeer(63, p2p.NewPeer(discover.NodeID{}, "empty", nil), io2))
	go pmEmpty.handle(pmEmpty.newPeer(63, p2p.NewPeer(discover.NodeID{}, "full", nil), io1))

	time.Sleep(250 * time.Millisecond)
	pmEmpty.synchronise(pmEmpty.peers.BestPeer())

	// Check that fast sync was disabled
	if atomic.LoadUint32(&pmEmpty.fastSync) == 1 {
		t.Fatalf("fast sync not disabled after successful synchronisation")
	}
}
