
package testing

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/p2p/simulations"
	"github.com/5uwifi/canchain/basis/p2p/simulations/adapters"
	"github.com/5uwifi/canchain/rpc"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/network"
	"github.com/5uwifi/canchain/basis/swarm/storage"
)

type Simulation struct {
	Net    *simulations.Network
	Stores []storage.ChunkStore
	Addrs  []network.Addr
	IDs    []discover.NodeID
}

func SetStores(addrs ...network.Addr) ([]storage.ChunkStore, func(), error) {
	var datadirs []string
	stores := make([]storage.ChunkStore, len(addrs))
	var err error
	for i, addr := range addrs {
		var datadir string
		datadir, err = ioutil.TempDir("", "streamer")
		if err != nil {
			break
		}
		var store storage.ChunkStore
		params := storage.NewDefaultLocalStoreParams()
		params.Init(datadir)
		params.BaseKey = addr.Over()
		store, err = storage.NewTestLocalStoreForAddr(params)
		if err != nil {
			break
		}
		datadirs = append(datadirs, datadir)
		stores[i] = store
	}
	teardown := func() {
		for i, datadir := range datadirs {
			stores[i].Close()
			os.RemoveAll(datadir)
		}
	}
	return stores, teardown, err
}

func NewAdapter(adapterType string, services adapters.Services) (adapter adapters.NodeAdapter, teardown func(), err error) {
	teardown = func() {}
	switch adapterType {
	case "sim":
		adapter = adapters.NewSimAdapter(services)
	case "exec":
		baseDir, err0 := ioutil.TempDir("", "swarm-test")
		if err0 != nil {
			return nil, teardown, err0
		}
		teardown = func() { os.RemoveAll(baseDir) }
		adapter = adapters.NewExecAdapter(baseDir)
	case "docker":
		adapter, err = adapters.NewDockerAdapter()
		if err != nil {
			return nil, teardown, err
		}
	default:
		return nil, teardown, errors.New("adapter needs to be one of sim, exec, docker")
	}
	return adapter, teardown, nil
}

func CheckResult(t *testing.T, result *simulations.StepResult, startedAt, finishedAt time.Time) {
	t.Logf("Simulation passed in %s", result.FinishedAt.Sub(result.StartedAt))
	if len(result.Passes) > 1 {
		var min, max time.Duration
		var sum int
		for _, pass := range result.Passes {
			duration := pass.Sub(result.StartedAt)
			if sum == 0 || duration < min {
				min = duration
			}
			if duration > max {
				max = duration
			}
			sum += int(duration.Nanoseconds())
		}
		t.Logf("Min: %s, Max: %s, Average: %s", min, max, time.Duration(sum/len(result.Passes))*time.Nanosecond)
	}
	t.Logf("Setup: %s, Shutdown: %s", result.StartedAt.Sub(startedAt), finishedAt.Sub(result.FinishedAt))
}

type RunConfig struct {
	Adapter         string
	Step            *simulations.Step
	NodeCount       int
	ConnLevel       int
	ToAddr          func(discover.NodeID) *network.BzzAddr
	Services        adapters.Services
	DefaultService  string
	EnableMsgEvents bool
}

func NewSimulation(conf *RunConfig) (*Simulation, func(), error) {
	// create network
	nodes := conf.NodeCount
	adapter, adapterTeardown, err := NewAdapter(conf.Adapter, conf.Services)
	if err != nil {
		return nil, adapterTeardown, err
	}
	defaultService := "streamer"
	if conf.DefaultService != "" {
		defaultService = conf.DefaultService
	}
	net := simulations.NewNetwork(adapter, &simulations.NetworkConfig{
		ID:             "0",
		DefaultService: defaultService,
	})
	teardown := func() {
		adapterTeardown()
		net.Shutdown()
	}
	ids := make([]discover.NodeID, nodes)
	addrs := make([]network.Addr, nodes)
	// start nodes
	for i := 0; i < nodes; i++ {
		nodeconf := adapters.RandomNodeConfig()
		nodeconf.EnableMsgEvents = conf.EnableMsgEvents
		node, err := net.NewNodeWithConfig(nodeconf)
		if err != nil {
			return nil, teardown, fmt.Errorf("error creating node: %s", err)
		}
		ids[i] = node.ID()
		addrs[i] = conf.ToAddr(ids[i])
	}
	// set nodes number of Stores available
	stores, storeTeardown, err := SetStores(addrs...)
	teardown = func() {
		net.Shutdown()
		adapterTeardown()
		storeTeardown()
	}
	if err != nil {
		return nil, teardown, err
	}
	s := &Simulation{
		Net:    net,
		Stores: stores,
		IDs:    ids,
		Addrs:  addrs,
	}
	return s, teardown, nil
}

func (s *Simulation) Run(ctx context.Context, conf *RunConfig) (*simulations.StepResult, error) {
	// bring up nodes, launch the servive
	nodes := conf.NodeCount
	conns := conf.ConnLevel
	for i := 0; i < nodes; i++ {
		if err := s.Net.Start(s.IDs[i]); err != nil {
			return nil, fmt.Errorf("error starting node %s: %s", s.IDs[i].TerminalString(), err)
		}
	}
	// run a simulation which connects the 10 nodes in a chain
	wg := sync.WaitGroup{}
	for i := range s.IDs {
		// collect the overlay addresses, to
		for j := 0; j < conns; j++ {
			var k int
			if j == 0 {
				k = i - 1
			} else {
				k = rand.Intn(len(s.IDs))
			}
			if i > 0 {
				wg.Add(1)
				go func(i, k int) {
					defer wg.Done()
					s.Net.Connect(s.IDs[i], s.IDs[k])
				}(i, k)
			}
		}
	}
	wg.Wait()
	log.Info(fmt.Sprintf("simulation with %v nodes", len(s.Addrs)))

	// create an only locally retrieving FileStore for the pivot node to test
	// if retriee requests have arrived
	result := simulations.NewSimulation(s.Net).Run(ctx, conf.Step)
	return result, nil
}

func WatchDisconnections(id discover.NodeID, client *rpc.Client, errc chan error, quitC chan struct{}) (doneC <-chan struct{}, err error) {
	events := make(chan *p2p.PeerEvent)
	sub, err := client.Subscribe(context.Background(), "admin", events, "peerEvents")
	if err != nil {
		return nil, fmt.Errorf("error getting peer events for node %v: %s", id, err)
	}
	c := make(chan struct{})
	go func() {
		defer func() {
			log.Trace("watch disconnections: unsubscribe", "id", id)
			sub.Unsubscribe()
			close(c)
		}()
		for {
			select {
			case <-quitC:
				return
			case e := <-events:
				if e.Type == p2p.PeerEventTypeDrop {
					select {
					case errc <- fmt.Errorf("peerEvent for node %v: %v", id, e):
					case <-quitC:
						return
					}
				}
			case err := <-sub.Err():
				if err != nil {
					select {
					case errc <- fmt.Errorf("error getting peer events for node %v: %v", id, err):
					case <-quitC:
						return
					}
				}
			}
		}
	}()
	return c, nil
}

func Trigger(d time.Duration, quitC chan struct{}, ids ...discover.NodeID) chan discover.NodeID {
	trigger := make(chan discover.NodeID)
	go func() {
		defer close(trigger)
		ticker := time.NewTicker(d)
		defer ticker.Stop()
		// we are only testing the pivot node (net.Nodes[0])
		for range ticker.C {
			for _, id := range ids {
				select {
				case trigger <- id:
				case <-quitC:
					return
				}
			}
		}
	}()
	return trigger
}

func (sim *Simulation) CallClient(id discover.NodeID, f func(*rpc.Client) error) error {
	node := sim.Net.GetNode(id)
	if node == nil {
		return fmt.Errorf("unknown node: %s", id)
	}
	client, err := node.Client()
	if err != nil {
		return fmt.Errorf("error getting node client: %s", err)
	}
	return f(client)
}
