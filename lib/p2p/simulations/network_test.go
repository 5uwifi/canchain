package simulations

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/simulations/adapters"
)

func TestNetworkSimulation(t *testing.T) {
	adapter := adapters.NewSimAdapter(adapters.Services{
		"test": newTestService,
	})
	network := NewNetwork(adapter, &NetworkConfig{
		DefaultService: "test",
	})
	defer network.Shutdown()
	nodeCount := 20
	ids := make([]cnode.ID, nodeCount)
	for i := 0; i < nodeCount; i++ {
		conf := adapters.RandomNodeConfig()
		node, err := network.NewNodeWithConfig(conf)
		if err != nil {
			t.Fatalf("error creating node: %s", err)
		}
		if err := network.Start(node.ID()); err != nil {
			t.Fatalf("error starting node: %s", err)
		}
		ids[i] = node.ID()
	}

	action := func(_ context.Context) error {
		for i, id := range ids {
			peerID := ids[(i+1)%len(ids)]
			if err := network.Connect(id, peerID); err != nil {
				return err
			}
		}
		return nil
	}
	check := func(ctx context.Context, id cnode.ID) (bool, error) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		node := network.GetNode(id)
		if node == nil {
			return false, fmt.Errorf("unknown node: %s", id)
		}

		client, err := node.Client()
		if err != nil {
			return false, err
		}
		var peerCount int64
		if err := client.CallContext(ctx, &peerCount, "test_peerCount"); err != nil {
			return false, err
		}
		switch {
		case peerCount < 2:
			return false, nil
		case peerCount == 2:
			return true, nil
		default:
			return false, fmt.Errorf("unexpected peerCount: %d", peerCount)
		}
	}

	timeout := 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	trigger := make(chan cnode.ID)
	go triggerChecks(ctx, ids, trigger, 100*time.Millisecond)

	result := NewSimulation(network).Run(ctx, &Step{
		Action:  action,
		Trigger: trigger,
		Expect: &Expectation{
			Nodes: ids,
			Check: check,
		},
	})
	if result.Error != nil {
		t.Fatalf("simulation failed: %s", result.Error)
	}

	snap, err := network.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != nodeCount {
		t.Fatalf("expected snapshot to contain %d nodes, got %d", nodeCount, len(snap.Nodes))
	}
	if len(snap.Conns) != nodeCount {
		t.Fatalf("expected snapshot to contain %d connections, got %d", nodeCount, len(snap.Conns))
	}
	for i, id := range ids {
		conn := snap.Conns[i]
		if conn.One != id {
			t.Fatalf("expected conn[%d].One to be %s, got %s", i, id, conn.One)
		}
		peerID := ids[(i+1)%len(ids)]
		if conn.Other != peerID {
			t.Fatalf("expected conn[%d].Other to be %s, got %s", i, peerID, conn.Other)
		}
	}
}

func triggerChecks(ctx context.Context, ids []cnode.ID, trigger chan cnode.ID, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			for _, id := range ids {
				select {
				case trigger <- id:
				case <-ctx.Done():
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
