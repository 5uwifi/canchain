//
// (at your option) any later version.
//
//

package stream

import (
	"errors"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/metrics"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/network"
	"github.com/5uwifi/canchain/basis/swarm/storage"
)

const (
	swarmChunkServerStreamName = "RETRIEVE_REQUEST"
	deliveryCap                = 32
)

var (
	processReceivedChunksCount    = metrics.NewRegisteredCounter("network.stream.received_chunks.count", nil)
	handleRetrieveRequestMsgCount = metrics.NewRegisteredCounter("network.stream.handle_retrieve_request_msg.count", nil)

	requestFromPeersCount     = metrics.NewRegisteredCounter("network.stream.request_from_peers.count", nil)
	requestFromPeersEachCount = metrics.NewRegisteredCounter("network.stream.request_from_peers_each.count", nil)
)

type Delivery struct {
	db       *storage.DBAPI
	overlay  network.Overlay
	receiveC chan *ChunkDeliveryMsg
	getPeer  func(discover.NodeID) *Peer
}

func NewDelivery(overlay network.Overlay, db *storage.DBAPI) *Delivery {
	d := &Delivery{
		db:       db,
		overlay:  overlay,
		receiveC: make(chan *ChunkDeliveryMsg, deliveryCap),
	}

	go d.processReceivedChunks()
	return d
}

type SwarmChunkServer struct {
	deliveryC  chan []byte
	batchC     chan []byte
	db         *storage.DBAPI
	currentLen uint64
	quit       chan struct{}
}

func NewSwarmChunkServer(db *storage.DBAPI) *SwarmChunkServer {
	s := &SwarmChunkServer{
		deliveryC: make(chan []byte, deliveryCap),
		batchC:    make(chan []byte),
		db:        db,
		quit:      make(chan struct{}),
	}
	go s.processDeliveries()
	return s
}

func (s *SwarmChunkServer) processDeliveries() {
	var hashes []byte
	var batchC chan []byte
	for {
		select {
		case <-s.quit:
			return
		case hash := <-s.deliveryC:
			hashes = append(hashes, hash...)
			batchC = s.batchC
		case batchC <- hashes:
			hashes = nil
			batchC = nil
		}
	}
}

func (s *SwarmChunkServer) SetNextBatch(_, _ uint64) (hashes []byte, from uint64, to uint64, proof *HandoverProof, err error) {
	select {
	case hashes = <-s.batchC:
	case <-s.quit:
		return
	}

	from = s.currentLen
	s.currentLen += uint64(len(hashes))
	to = s.currentLen
	return
}

func (s *SwarmChunkServer) Close() {
	close(s.quit)
}

func (s *SwarmChunkServer) GetData(key []byte) ([]byte, error) {
	chunk, err := s.db.Get(storage.Address(key))
	if err == storage.ErrFetching {
		<-chunk.ReqC
	} else if err != nil {
		return nil, err
	}
	return chunk.SData, nil
}

type RetrieveRequestMsg struct {
	Addr      storage.Address
	SkipCheck bool
}

func (d *Delivery) handleRetrieveRequestMsg(sp *Peer, req *RetrieveRequestMsg) error {
	log.Trace("received request", "peer", sp.ID(), "hash", req.Addr)
	handleRetrieveRequestMsgCount.Inc(1)

	s, err := sp.getServer(NewStream(swarmChunkServerStreamName, "", false))
	if err != nil {
		return err
	}
	streamer := s.Server.(*SwarmChunkServer)
	chunk, created := d.db.GetOrCreateRequest(req.Addr)
	if chunk.ReqC != nil {
		if created {
			if err := d.RequestFromPeers(chunk.Addr[:], true, sp.ID()); err != nil {
				log.Warn("unable to forward chunk request", "peer", sp.ID(), "key", chunk.Addr, "err", err)
				chunk.SetErrored(storage.ErrChunkForward)
				return nil
			}
		}
		go func() {
			t := time.NewTimer(10 * time.Minute)
			defer t.Stop()

			log.Debug("waiting delivery", "peer", sp.ID(), "hash", req.Addr, "node", common.Bytes2Hex(d.overlay.BaseAddr()), "created", created)
			start := time.Now()
			select {
			case <-chunk.ReqC:
				log.Debug("retrieve request ReqC closed", "peer", sp.ID(), "hash", req.Addr, "time", time.Since(start))
			case <-t.C:
				log.Debug("retrieve request timeout", "peer", sp.ID(), "hash", req.Addr)
				chunk.SetErrored(storage.ErrChunkTimeout)
				return
			}
			chunk.SetErrored(nil)

			if req.SkipCheck {
				err := sp.Deliver(chunk, s.priority)
				if err != nil {
					log.Warn("ERROR in handleRetrieveRequestMsg, DROPPING peer!", "err", err)
					sp.Drop(err)
				}
			}
			streamer.deliveryC <- chunk.Addr[:]
		}()
		return nil
	}
	// TODO: call the retrieve function of the outgoing syncer
	if req.SkipCheck {
		log.Trace("deliver", "peer", sp.ID(), "hash", chunk.Addr)
		if length := len(chunk.SData); length < 9 {
			log.Error("Chunk.SData to deliver is too short", "len(chunk.SData)", length, "address", chunk.Addr)
		}
		return sp.Deliver(chunk, s.priority)
	}
	streamer.deliveryC <- chunk.Addr[:]
	return nil
}

type ChunkDeliveryMsg struct {
	Addr  storage.Address
	SData []byte // the stored chunk Data (incl size)
	peer  *Peer  // set in handleChunkDeliveryMsg
}

func (d *Delivery) handleChunkDeliveryMsg(sp *Peer, req *ChunkDeliveryMsg) error {
	req.peer = sp
	d.receiveC <- req
	return nil
}

func (d *Delivery) processReceivedChunks() {
R:
	for req := range d.receiveC {
		processReceivedChunksCount.Inc(1)

		// this should be has locally
		chunk, err := d.db.Get(req.Addr)
		if err == nil {
			continue R
		}
		if err != storage.ErrFetching {
			log.Error("processReceivedChunks db error", "addr", req.Addr, "err", err, "chunk", chunk)
			continue R
		}
		select {
		case <-chunk.ReqC:
			log.Error("someone else delivered?", "hash", chunk.Addr.Hex())
			continue R
		default:
		}
		chunk.SData = req.SData
		d.db.Put(chunk)

		go func(req *ChunkDeliveryMsg) {
			err := chunk.WaitToStore()
			if err == storage.ErrChunkInvalid {
				req.peer.Drop(err)
			}
		}(req)
	}
}

func (d *Delivery) RequestFromPeers(hash []byte, skipCheck bool, peersToSkip ...discover.NodeID) error {
	var success bool
	var err error
	requestFromPeersCount.Inc(1)
	d.overlay.EachConn(hash, 255, func(p network.OverlayConn, po int, nn bool) bool {
		spId := p.(network.Peer).ID()
		for _, p := range peersToSkip {
			if p == spId {
				log.Trace("Delivery.RequestFromPeers: skip peer", "peer", spId)
				return true
			}
		}
		sp := d.getPeer(spId)
		if sp == nil {
			log.Warn("Delivery.RequestFromPeers: peer not found", "id", spId)
			return true
		}
		// TODO: skip light nodes that do not accept retrieve requests
		err = sp.SendPriority(&RetrieveRequestMsg{
			Addr:      hash,
			SkipCheck: skipCheck,
		}, Top)
		if err != nil {
			return true
		}
		requestFromPeersEachCount.Inc(1)
		success = true
		return false
	})
	if success {
		return nil
	}
	return errors.New("no peer found")
}
