//
// (at your option) any later version.
//
//

package whisperv6

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/rlp"
	set "gopkg.in/fatih/set.v0"
)

type Peer struct {
	host *Whisper
	peer *p2p.Peer
	ws   p2p.MsgReadWriter

	trusted        bool
	powRequirement float64
	bloomMu        sync.Mutex
	bloomFilter    []byte
	fullNode       bool

	known *set.Set // Messages already known by the peer to avoid wasting bandwidth

	quit chan struct{}
}

func newPeer(host *Whisper, remote *p2p.Peer, rw p2p.MsgReadWriter) *Peer {
	return &Peer{
		host:           host,
		peer:           remote,
		ws:             rw,
		trusted:        false,
		powRequirement: 0.0,
		known:          set.New(),
		quit:           make(chan struct{}),
		bloomFilter:    MakeFullNodeBloom(),
		fullNode:       true,
	}
}

func (peer *Peer) start() {
	go peer.update()
	log4j.Trace("start", "peer", peer.ID())
}

func (peer *Peer) stop() {
	close(peer.quit)
	log4j.Trace("stop", "peer", peer.ID())
}

func (peer *Peer) handshake() error {
	// Send the handshake status message asynchronously
	errc := make(chan error, 1)
	go func() {
		pow := peer.host.MinPow()
		powConverted := math.Float64bits(pow)
		bloom := peer.host.BloomFilter()
		errc <- p2p.SendItems(peer.ws, statusCode, ProtocolVersion, powConverted, bloom)
	}()

	// Fetch the remote status packet and verify protocol match
	packet, err := peer.ws.ReadMsg()
	if err != nil {
		return err
	}
	if packet.Code != statusCode {
		return fmt.Errorf("peer [%x] sent packet %x before status packet", peer.ID(), packet.Code)
	}
	s := rlp.NewStream(packet.Payload, uint64(packet.Size))
	_, err = s.List()
	if err != nil {
		return fmt.Errorf("peer [%x] sent bad status message: %v", peer.ID(), err)
	}
	peerVersion, err := s.Uint()
	if err != nil {
		return fmt.Errorf("peer [%x] sent bad status message (unable to decode version): %v", peer.ID(), err)
	}
	if peerVersion != ProtocolVersion {
		return fmt.Errorf("peer [%x]: protocol version mismatch %d != %d", peer.ID(), peerVersion, ProtocolVersion)
	}

	// only version is mandatory, subsequent parameters are optional
	powRaw, err := s.Uint()
	if err == nil {
		pow := math.Float64frombits(powRaw)
		if math.IsInf(pow, 0) || math.IsNaN(pow) || pow < 0.0 {
			return fmt.Errorf("peer [%x] sent bad status message: invalid pow", peer.ID())
		}
		peer.powRequirement = pow

		var bloom []byte
		err = s.Decode(&bloom)
		if err == nil {
			sz := len(bloom)
			if sz != BloomFilterSize && sz != 0 {
				return fmt.Errorf("peer [%x] sent bad status message: wrong bloom filter size %d", peer.ID(), sz)
			}
			peer.setBloomFilter(bloom)
		}
	}

	if err := <-errc; err != nil {
		return fmt.Errorf("peer [%x] failed to send status packet: %v", peer.ID(), err)
	}
	return nil
}

func (peer *Peer) update() {
	// Start the tickers for the updates
	expire := time.NewTicker(expirationCycle)
	transmit := time.NewTicker(transmissionCycle)

	// Loop and transmit until termination is requested
	for {
		select {
		case <-expire.C:
			peer.expire()

		case <-transmit.C:
			if err := peer.broadcast(); err != nil {
				log4j.Trace("broadcast failed", "reason", err, "peer", peer.ID())
				return
			}

		case <-peer.quit:
			return
		}
	}
}

func (peer *Peer) mark(envelope *Envelope) {
	peer.known.Add(envelope.Hash())
}

func (peer *Peer) marked(envelope *Envelope) bool {
	return peer.known.Has(envelope.Hash())
}

func (peer *Peer) expire() {
	unmark := make(map[common.Hash]struct{})
	peer.known.Each(func(v interface{}) bool {
		if !peer.host.isEnvelopeCached(v.(common.Hash)) {
			unmark[v.(common.Hash)] = struct{}{}
		}
		return true
	})
	// Dump all known but no longer cached
	for hash := range unmark {
		peer.known.Remove(hash)
	}
}

func (peer *Peer) broadcast() error {
	envelopes := peer.host.Envelopes()
	bundle := make([]*Envelope, 0, len(envelopes))
	for _, envelope := range envelopes {
		if !peer.marked(envelope) && envelope.PoW() >= peer.powRequirement && peer.bloomMatch(envelope) {
			bundle = append(bundle, envelope)
		}
	}

	if len(bundle) > 0 {
		// transmit the batch of envelopes
		if err := p2p.Send(peer.ws, messagesCode, bundle); err != nil {
			return err
		}

		// mark envelopes only if they were successfully sent
		for _, e := range bundle {
			peer.mark(e)
		}

		log4j.Trace("broadcast", "num. messages", len(bundle))
	}
	return nil
}

func (peer *Peer) ID() []byte {
	id := peer.peer.ID()
	return id[:]
}

func (peer *Peer) notifyAboutPowRequirementChange(pow float64) error {
	i := math.Float64bits(pow)
	return p2p.Send(peer.ws, powRequirementCode, i)
}

func (peer *Peer) notifyAboutBloomFilterChange(bloom []byte) error {
	return p2p.Send(peer.ws, bloomFilterExCode, bloom)
}

func (peer *Peer) bloomMatch(env *Envelope) bool {
	peer.bloomMu.Lock()
	defer peer.bloomMu.Unlock()
	return peer.fullNode || BloomFilterMatch(peer.bloomFilter, env.Bloom())
}

func (peer *Peer) setBloomFilter(bloom []byte) {
	peer.bloomMu.Lock()
	defer peer.bloomMu.Unlock()
	peer.bloomFilter = bloom
	peer.fullNode = isFullNode(bloom)
	if peer.fullNode && peer.bloomFilter == nil {
		peer.bloomFilter = MakeFullNodeBloom()
	}
}

func MakeFullNodeBloom() []byte {
	bloom := make([]byte, BloomFilterSize)
	for i := 0; i < BloomFilterSize; i++ {
		bloom[i] = 0xFF
	}
	return bloom
}
