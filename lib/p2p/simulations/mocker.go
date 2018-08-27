package simulations

import (
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/lib/p2p/simulations/adapters"
)

var mockerList = map[string]func(net *Network, quit chan struct{}, nodeCount int){
	"startStop":     startStop,
	"probabilistic": probabilistic,
	"boot":          boot,
}

func LookupMocker(mockerType string) func(net *Network, quit chan struct{}, nodeCount int) {
	return mockerList[mockerType]
}

func GetMockerList() []string {
	list := make([]string, 0, len(mockerList))
	for k := range mockerList {
		list = append(list, k)
	}
	return list
}

func boot(net *Network, quit chan struct{}, nodeCount int) {
	_, err := connectNodesInRing(net, nodeCount)
	if err != nil {
		panic("Could not startup node network for mocker")
	}
}

func startStop(net *Network, quit chan struct{}, nodeCount int) {
	nodes, err := connectNodesInRing(net, nodeCount)
	if err != nil {
		panic("Could not startup node network for mocker")
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-quit:
			log4j.Info("Terminating simulation loop")
			return
		case <-tick.C:
			id := nodes[rand.Intn(len(nodes))]
			log4j.Info("stopping node", "id", id)
			if err := net.Stop(id); err != nil {
				log4j.Error("error stopping node", "id", id, "err", err)
				return
			}

			select {
			case <-quit:
				log4j.Info("Terminating simulation loop")
				return
			case <-time.After(3 * time.Second):
			}

			log4j.Debug("starting node", "id", id)
			if err := net.Start(id); err != nil {
				log4j.Error("error starting node", "id", id, "err", err)
				return
			}
		}
	}
}

func probabilistic(net *Network, quit chan struct{}, nodeCount int) {
	nodes, err := connectNodesInRing(net, nodeCount)
	if err != nil {
		select {
		case <-quit:
			return
		default:
			panic("Could not startup node network for mocker")
		}
	}
	for {
		select {
		case <-quit:
			log4j.Info("Terminating simulation loop")
			return
		default:
		}
		var lowid, highid int
		var wg sync.WaitGroup
		randWait := time.Duration(rand.Intn(5000)+1000) * time.Millisecond
		rand1 := rand.Intn(nodeCount - 1)
		rand2 := rand.Intn(nodeCount - 1)
		if rand1 < rand2 {
			lowid = rand1
			highid = rand2
		} else if rand1 > rand2 {
			highid = rand1
			lowid = rand2
		} else {
			if rand1 == 0 {
				rand2 = 9
			} else if rand1 == 9 {
				rand1 = 0
			}
			lowid = rand1
			highid = rand2
		}
		var steps = highid - lowid
		wg.Add(steps)
		for i := lowid; i < highid; i++ {
			select {
			case <-quit:
				log4j.Info("Terminating simulation loop")
				return
			case <-time.After(randWait):
			}
			log4j.Debug(fmt.Sprintf("node %v shutting down", nodes[i]))
			err := net.Stop(nodes[i])
			if err != nil {
				log4j.Error("Error stopping node", "node", nodes[i])
				wg.Done()
				continue
			}
			go func(id discover.NodeID) {
				time.Sleep(randWait)
				err := net.Start(id)
				if err != nil {
					log4j.Error("Error starting node", "node", id)
				}
				wg.Done()
			}(nodes[i])
		}
		wg.Wait()
	}

}

func connectNodesInRing(net *Network, nodeCount int) ([]discover.NodeID, error) {
	ids := make([]discover.NodeID, nodeCount)
	for i := 0; i < nodeCount; i++ {
		conf := adapters.RandomNodeConfig()
		node, err := net.NewNodeWithConfig(conf)
		if err != nil {
			log4j.Error("Error creating a node!", "err", err)
			return nil, err
		}
		ids[i] = node.ID()
	}

	for _, id := range ids {
		if err := net.Start(id); err != nil {
			log4j.Error("Error starting a node!", "err", err)
			return nil, err
		}
		log4j.Debug(fmt.Sprintf("node %v starting up", id))
	}
	for i, id := range ids {
		peerID := ids[(i+1)%len(ids)]
		if err := net.Connect(id, peerID); err != nil {
			log4j.Error("Error connecting a node to a peer!", "err", err)
			return nil, err
		}
	}

	return ids, nil
}
