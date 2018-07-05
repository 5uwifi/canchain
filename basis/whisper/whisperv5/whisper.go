//
// (at your option) any later version.
//
//

package whisperv5

import (
	"bytes"
	"crypto/ecdsa"
	crand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/crypto"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/rpc"
	"github.com/syndtr/goleveldb/leveldb/errors"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/sync/syncmap"
	set "gopkg.in/fatih/set.v0"
)

type Statistics struct {
	messagesCleared      int
	memoryCleared        int
	memoryUsed           int
	cycles               int
	totalMessagesCleared int
}

const (
	minPowIdx     = iota // Minimal PoW required by the whisper node
	maxMsgSizeIdx = iota // Maximal message length allowed by the whisper node
	overflowIdx   = iota // Indicator of message queue overflow
)

type Whisper struct {
	protocol p2p.Protocol // Protocol description and parameters
	filters  *Filters     // Message filters installed with Subscribe function

	privateKeys map[string]*ecdsa.PrivateKey // Private key storage
	symKeys     map[string][]byte            // Symmetric key storage
	keyMu       sync.RWMutex                 // Mutex associated with key storages

	poolMu      sync.RWMutex              // Mutex to sync the message and expiration pools
	envelopes   map[common.Hash]*Envelope // Pool of envelopes currently tracked by this node
	expirations map[uint32]*set.SetNonTS  // Message expiration pool

	peerMu sync.RWMutex       // Mutex to sync the active peer set
	peers  map[*Peer]struct{} // Set of currently active peers

	messageQueue chan *Envelope // Message queue for normal whisper messages
	p2pMsgQueue  chan *Envelope // Message queue for peer-to-peer messages (not to be forwarded any further)
	quit         chan struct{}  // Channel used for graceful exit

	settings syncmap.Map // holds configuration settings that can be dynamically changed

	statsMu sync.Mutex // guard stats
	stats   Statistics // Statistics of whisper node

	mailServer MailServer // MailServer interface
}

func New(cfg *Config) *Whisper {
	if cfg == nil {
		cfg = &DefaultConfig
	}

	whisper := &Whisper{
		privateKeys:  make(map[string]*ecdsa.PrivateKey),
		symKeys:      make(map[string][]byte),
		envelopes:    make(map[common.Hash]*Envelope),
		expirations:  make(map[uint32]*set.SetNonTS),
		peers:        make(map[*Peer]struct{}),
		messageQueue: make(chan *Envelope, messageQueueLimit),
		p2pMsgQueue:  make(chan *Envelope, messageQueueLimit),
		quit:         make(chan struct{}),
	}

	whisper.filters = NewFilters(whisper)

	whisper.settings.Store(minPowIdx, cfg.MinimumAcceptedPOW)
	whisper.settings.Store(maxMsgSizeIdx, cfg.MaxMessageSize)
	whisper.settings.Store(overflowIdx, false)

	// p2p whisper sub protocol handler
	whisper.protocol = p2p.Protocol{
		Name:    ProtocolName,
		Version: uint(ProtocolVersion),
		Length:  NumberOfMessageCodes,
		Run:     whisper.HandlePeer,
		NodeInfo: func() interface{} {
			return map[string]interface{}{
				"version":        ProtocolVersionStr,
				"maxMessageSize": whisper.MaxMessageSize(),
				"minimumPoW":     whisper.MinPow(),
			}
		},
	}

	return whisper
}

func (w *Whisper) MinPow() float64 {
	val, _ := w.settings.Load(minPowIdx)
	return val.(float64)
}

func (w *Whisper) MaxMessageSize() uint32 {
	val, _ := w.settings.Load(maxMsgSizeIdx)
	return val.(uint32)
}

func (w *Whisper) Overflow() bool {
	val, _ := w.settings.Load(overflowIdx)
	return val.(bool)
}

func (w *Whisper) APIs() []rpc.API {
	return []rpc.API{
		{
			Namespace: ProtocolName,
			Version:   ProtocolVersionStr,
			Service:   NewPublicWhisperAPI(w),
			Public:    true,
		},
	}
}

func (w *Whisper) RegisterServer(server MailServer) {
	w.mailServer = server
}

func (w *Whisper) Protocols() []p2p.Protocol {
	return []p2p.Protocol{w.protocol}
}

func (w *Whisper) Version() uint {
	return w.protocol.Version
}

func (w *Whisper) SetMaxMessageSize(size uint32) error {
	if size > MaxMessageSize {
		return fmt.Errorf("message size too large [%d>%d]", size, MaxMessageSize)
	}
	w.settings.Store(maxMsgSizeIdx, size)
	return nil
}

func (w *Whisper) SetMinimumPoW(val float64) error {
	if val <= 0.0 {
		return fmt.Errorf("invalid PoW: %f", val)
	}
	w.settings.Store(minPowIdx, val)
	return nil
}

func (w *Whisper) getPeer(peerID []byte) (*Peer, error) {
	w.peerMu.Lock()
	defer w.peerMu.Unlock()
	for p := range w.peers {
		id := p.peer.ID()
		if bytes.Equal(peerID, id[:]) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("Could not find peer with ID: %x", peerID)
}

func (w *Whisper) AllowP2PMessagesFromPeer(peerID []byte) error {
	p, err := w.getPeer(peerID)
	if err != nil {
		return err
	}
	p.trusted = true
	return nil
}

func (w *Whisper) RequestHistoricMessages(peerID []byte, envelope *Envelope) error {
	p, err := w.getPeer(peerID)
	if err != nil {
		return err
	}
	p.trusted = true
	return p2p.Send(p.ws, p2pRequestCode, envelope)
}

func (w *Whisper) SendP2PMessage(peerID []byte, envelope *Envelope) error {
	p, err := w.getPeer(peerID)
	if err != nil {
		return err
	}
	return w.SendP2PDirect(p, envelope)
}

func (w *Whisper) SendP2PDirect(peer *Peer, envelope *Envelope) error {
	return p2p.Send(peer.ws, p2pCode, envelope)
}

func (w *Whisper) NewKeyPair() (string, error) {
	key, err := crypto.GenerateKey()
	if err != nil || !validatePrivateKey(key) {
		key, err = crypto.GenerateKey() // retry once
	}
	if err != nil {
		return "", err
	}
	if !validatePrivateKey(key) {
		return "", fmt.Errorf("failed to generate valid key")
	}

	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	w.keyMu.Lock()
	defer w.keyMu.Unlock()

	if w.privateKeys[id] != nil {
		return "", fmt.Errorf("failed to generate unique ID")
	}
	w.privateKeys[id] = key
	return id, nil
}

func (w *Whisper) DeleteKeyPair(key string) bool {
	w.keyMu.Lock()
	defer w.keyMu.Unlock()

	if w.privateKeys[key] != nil {
		delete(w.privateKeys, key)
		return true
	}
	return false
}

func (w *Whisper) AddKeyPair(key *ecdsa.PrivateKey) (string, error) {
	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	w.keyMu.Lock()
	w.privateKeys[id] = key
	w.keyMu.Unlock()

	return id, nil
}

func (w *Whisper) HasKeyPair(id string) bool {
	w.keyMu.RLock()
	defer w.keyMu.RUnlock()
	return w.privateKeys[id] != nil
}

func (w *Whisper) GetPrivateKey(id string) (*ecdsa.PrivateKey, error) {
	w.keyMu.RLock()
	defer w.keyMu.RUnlock()
	key := w.privateKeys[id]
	if key == nil {
		return nil, fmt.Errorf("invalid id")
	}
	return key, nil
}

func (w *Whisper) GenerateSymKey() (string, error) {
	key := make([]byte, aesKeyLength)
	_, err := crand.Read(key)
	if err != nil {
		return "", err
	} else if !validateSymmetricKey(key) {
		return "", fmt.Errorf("error in GenerateSymKey: crypto/rand failed to generate random data")
	}

	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	w.keyMu.Lock()
	defer w.keyMu.Unlock()

	if w.symKeys[id] != nil {
		return "", fmt.Errorf("failed to generate unique ID")
	}
	w.symKeys[id] = key
	return id, nil
}

func (w *Whisper) AddSymKeyDirect(key []byte) (string, error) {
	if len(key) != aesKeyLength {
		return "", fmt.Errorf("wrong key size: %d", len(key))
	}

	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	w.keyMu.Lock()
	defer w.keyMu.Unlock()

	if w.symKeys[id] != nil {
		return "", fmt.Errorf("failed to generate unique ID")
	}
	w.symKeys[id] = key
	return id, nil
}

func (w *Whisper) AddSymKeyFromPassword(password string) (string, error) {
	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}
	if w.HasSymKey(id) {
		return "", fmt.Errorf("failed to generate unique ID")
	}

	derived, err := deriveKeyMaterial([]byte(password), EnvelopeVersion)
	if err != nil {
		return "", err
	}

	w.keyMu.Lock()
	defer w.keyMu.Unlock()

	// double check is necessary, because deriveKeyMaterial() is very slow
	if w.symKeys[id] != nil {
		return "", fmt.Errorf("critical error: failed to generate unique ID")
	}
	w.symKeys[id] = derived
	return id, nil
}

func (w *Whisper) HasSymKey(id string) bool {
	w.keyMu.RLock()
	defer w.keyMu.RUnlock()
	return w.symKeys[id] != nil
}

func (w *Whisper) DeleteSymKey(id string) bool {
	w.keyMu.Lock()
	defer w.keyMu.Unlock()
	if w.symKeys[id] != nil {
		delete(w.symKeys, id)
		return true
	}
	return false
}

func (w *Whisper) GetSymKey(id string) ([]byte, error) {
	w.keyMu.RLock()
	defer w.keyMu.RUnlock()
	if w.symKeys[id] != nil {
		return w.symKeys[id], nil
	}
	return nil, fmt.Errorf("non-existent key ID")
}

func (w *Whisper) Subscribe(f *Filter) (string, error) {
	return w.filters.Install(f)
}

func (w *Whisper) GetFilter(id string) *Filter {
	return w.filters.Get(id)
}

func (w *Whisper) Unsubscribe(id string) error {
	ok := w.filters.Uninstall(id)
	if !ok {
		return fmt.Errorf("Unsubscribe: Invalid ID")
	}
	return nil
}

func (w *Whisper) Send(envelope *Envelope) error {
	ok, err := w.add(envelope)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("failed to add envelope")
	}
	return err
}

func (w *Whisper) Start(*p2p.Server) error {
	log4j.Info("started whisper v." + ProtocolVersionStr)
	go w.update()

	numCPU := runtime.NumCPU()
	for i := 0; i < numCPU; i++ {
		go w.processQueue()
	}

	return nil
}

func (w *Whisper) Stop() error {
	close(w.quit)
	log4j.Info("whisper stopped")
	return nil
}

func (w *Whisper) HandlePeer(peer *p2p.Peer, rw p2p.MsgReadWriter) error {
	// Create the new peer and start tracking it
	whisperPeer := newPeer(w, peer, rw)

	w.peerMu.Lock()
	w.peers[whisperPeer] = struct{}{}
	w.peerMu.Unlock()

	defer func() {
		w.peerMu.Lock()
		delete(w.peers, whisperPeer)
		w.peerMu.Unlock()
	}()

	// Run the peer handshake and state updates
	if err := whisperPeer.handshake(); err != nil {
		return err
	}
	whisperPeer.start()
	defer whisperPeer.stop()

	return w.runMessageLoop(whisperPeer, rw)
}

func (w *Whisper) runMessageLoop(p *Peer, rw p2p.MsgReadWriter) error {
	for {
		// fetch the next packet
		packet, err := rw.ReadMsg()
		if err != nil {
			log4j.Info("message loop", "peer", p.peer.ID(), "err", err)
			return err
		}
		if packet.Size > w.MaxMessageSize() {
			log4j.Warn("oversized message received", "peer", p.peer.ID())
			return errors.New("oversized message received")
		}

		switch packet.Code {
		case statusCode:
			// this should not happen, but no need to panic; just ignore this message.
			log4j.Warn("unxepected status message received", "peer", p.peer.ID())
		case messagesCode:
			// decode the contained envelopes
			var envelope Envelope
			if err := packet.Decode(&envelope); err != nil {
				log4j.Warn("failed to decode envelope, peer will be disconnected", "peer", p.peer.ID(), "err", err)
				return errors.New("invalid envelope")
			}
			cached, err := w.add(&envelope)
			if err != nil {
				log4j.Warn("bad envelope received, peer will be disconnected", "peer", p.peer.ID(), "err", err)
				return errors.New("invalid envelope")
			}
			if cached {
				p.mark(&envelope)
			}
		case p2pCode:
			// peer-to-peer message, sent directly to peer bypassing PoW checks, etc.
			// this message is not supposed to be forwarded to other peers, and
			// therefore might not satisfy the PoW, expiry and other requirements.
			// these messages are only accepted from the trusted peer.
			if p.trusted {
				var envelope Envelope
				if err := packet.Decode(&envelope); err != nil {
					log4j.Warn("failed to decode direct message, peer will be disconnected", "peer", p.peer.ID(), "err", err)
					return errors.New("invalid direct message")
				}
				w.postEvent(&envelope, true)
			}
		case p2pRequestCode:
			// Must be processed if mail server is implemented. Otherwise ignore.
			if w.mailServer != nil {
				var request Envelope
				if err := packet.Decode(&request); err != nil {
					log4j.Warn("failed to decode p2p request message, peer will be disconnected", "peer", p.peer.ID(), "err", err)
					return errors.New("invalid p2p request")
				}
				w.mailServer.DeliverMail(p, &request)
			}
		default:
			// New message types might be implemented in the future versions of Whisper.
			// For forward compatibility, just ignore.
		}

		packet.Discard()
	}
}

func (w *Whisper) add(envelope *Envelope) (bool, error) {
	now := uint32(time.Now().Unix())
	sent := envelope.Expiry - envelope.TTL

	if sent > now {
		if sent-SynchAllowance > now {
			return false, fmt.Errorf("envelope created in the future [%x]", envelope.Hash())
		}
		// recalculate PoW, adjusted for the time difference, plus one second for latency
		envelope.calculatePoW(sent - now + 1)
	}

	if envelope.Expiry < now {
		if envelope.Expiry+SynchAllowance*2 < now {
			return false, fmt.Errorf("very old message")
		}
		log4j.Debug("expired envelope dropped", "hash", envelope.Hash().Hex())
		return false, nil // drop envelope without error
	}

	if uint32(envelope.size()) > w.MaxMessageSize() {
		return false, fmt.Errorf("huge messages are not allowed [%x]", envelope.Hash())
	}

	if len(envelope.Version) > 4 {
		return false, fmt.Errorf("oversized version [%x]", envelope.Hash())
	}

	aesNonceSize := len(envelope.AESNonce)
	if aesNonceSize != 0 && aesNonceSize != AESNonceLength {
		// the standard AES GCM nonce size is 12 bytes,
		// but constant gcmStandardNonceSize cannot be accessed (not exported)
		return false, fmt.Errorf("wrong size of AESNonce: %d bytes [env: %x]", aesNonceSize, envelope.Hash())
	}

	if envelope.PoW() < w.MinPow() {
		log4j.Debug("envelope with low PoW dropped", "PoW", envelope.PoW(), "hash", envelope.Hash().Hex())
		return false, nil // drop envelope without error
	}

	hash := envelope.Hash()

	w.poolMu.Lock()
	_, alreadyCached := w.envelopes[hash]
	if !alreadyCached {
		w.envelopes[hash] = envelope
		if w.expirations[envelope.Expiry] == nil {
			w.expirations[envelope.Expiry] = set.NewNonTS()
		}
		if !w.expirations[envelope.Expiry].Has(hash) {
			w.expirations[envelope.Expiry].Add(hash)
		}
	}
	w.poolMu.Unlock()

	if alreadyCached {
		log4j.Trace("whisper envelope already cached", "hash", envelope.Hash().Hex())
	} else {
		log4j.Trace("cached whisper envelope", "hash", envelope.Hash().Hex())
		w.statsMu.Lock()
		w.stats.memoryUsed += envelope.size()
		w.statsMu.Unlock()
		w.postEvent(envelope, false) // notify the local node about the new message
		if w.mailServer != nil {
			w.mailServer.Archive(envelope)
		}
	}
	return true, nil
}

func (w *Whisper) postEvent(envelope *Envelope, isP2P bool) {
	// if the version of incoming message is higher than
	// currently supported version, we can not decrypt it,
	// and therefore just ignore this message
	if envelope.Ver() <= EnvelopeVersion {
		if isP2P {
			w.p2pMsgQueue <- envelope
		} else {
			w.checkOverflow()
			w.messageQueue <- envelope
		}
	}
}

func (w *Whisper) checkOverflow() {
	queueSize := len(w.messageQueue)

	if queueSize == messageQueueLimit {
		if !w.Overflow() {
			w.settings.Store(overflowIdx, true)
			log4j.Warn("message queue overflow")
		}
	} else if queueSize <= messageQueueLimit/2 {
		if w.Overflow() {
			w.settings.Store(overflowIdx, false)
			log4j.Warn("message queue overflow fixed (back to normal)")
		}
	}
}

func (w *Whisper) processQueue() {
	var e *Envelope
	for {
		select {
		case <-w.quit:
			return

		case e = <-w.messageQueue:
			w.filters.NotifyWatchers(e, false)

		case e = <-w.p2pMsgQueue:
			w.filters.NotifyWatchers(e, true)
		}
	}
}

func (w *Whisper) update() {
	// Start a ticker to check for expirations
	expire := time.NewTicker(expirationCycle)

	// Repeat updates until termination is requested
	for {
		select {
		case <-expire.C:
			w.expire()

		case <-w.quit:
			return
		}
	}
}

func (w *Whisper) expire() {
	w.poolMu.Lock()
	defer w.poolMu.Unlock()

	w.statsMu.Lock()
	defer w.statsMu.Unlock()
	w.stats.reset()
	now := uint32(time.Now().Unix())
	for expiry, hashSet := range w.expirations {
		if expiry < now {
			// Dump all expired messages and remove timestamp
			hashSet.Each(func(v interface{}) bool {
				sz := w.envelopes[v.(common.Hash)].size()
				delete(w.envelopes, v.(common.Hash))
				w.stats.messagesCleared++
				w.stats.memoryCleared += sz
				w.stats.memoryUsed -= sz
				return true
			})
			w.expirations[expiry].Clear()
			delete(w.expirations, expiry)
		}
	}
}

func (w *Whisper) Stats() Statistics {
	w.statsMu.Lock()
	defer w.statsMu.Unlock()

	return w.stats
}

func (w *Whisper) Envelopes() []*Envelope {
	w.poolMu.RLock()
	defer w.poolMu.RUnlock()

	all := make([]*Envelope, 0, len(w.envelopes))
	for _, envelope := range w.envelopes {
		all = append(all, envelope)
	}
	return all
}

func (w *Whisper) Messages(id string) []*ReceivedMessage {
	result := make([]*ReceivedMessage, 0)
	w.poolMu.RLock()
	defer w.poolMu.RUnlock()

	if filter := w.filters.Get(id); filter != nil {
		for _, env := range w.envelopes {
			msg := filter.processEnvelope(env)
			if msg != nil {
				result = append(result, msg)
			}
		}
	}
	return result
}

func (w *Whisper) isEnvelopeCached(hash common.Hash) bool {
	w.poolMu.Lock()
	defer w.poolMu.Unlock()

	_, exist := w.envelopes[hash]
	return exist
}

func (s *Statistics) reset() {
	s.cycles++
	s.totalMessagesCleared += s.messagesCleared

	s.memoryCleared = 0
	s.messagesCleared = 0
}

func ValidatePublicKey(k *ecdsa.PublicKey) bool {
	return k != nil && k.X != nil && k.Y != nil && k.X.Sign() != 0 && k.Y.Sign() != 0
}

func validatePrivateKey(k *ecdsa.PrivateKey) bool {
	if k == nil || k.D == nil || k.D.Sign() == 0 {
		return false
	}
	return ValidatePublicKey(&k.PublicKey)
}

func validateSymmetricKey(k []byte) bool {
	return len(k) > 0 && !containsOnlyZeros(k)
}

func containsOnlyZeros(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func bytesToUintLittleEndian(b []byte) (res uint64) {
	mul := uint64(1)
	for i := 0; i < len(b); i++ {
		res += uint64(b[i]) * mul
		mul *= 256
	}
	return res
}

func BytesToUintBigEndian(b []byte) (res uint64) {
	for i := 0; i < len(b); i++ {
		res *= 256
		res += uint64(b[i])
	}
	return res
}

func deriveKeyMaterial(key []byte, version uint64) (derivedKey []byte, err error) {
	if version == 0 {
		// kdf should run no less than 0.1 seconds on average compute,
		// because it's a once in a session experience
		derivedKey := pbkdf2.Key(key, nil, 65356, aesKeyLength, sha256.New)
		return derivedKey, nil
	}
	return nil, unknownVersionError(version)
}

func GenerateRandomID() (id string, err error) {
	buf := make([]byte, keyIdSize)
	_, err = crand.Read(buf)
	if err != nil {
		return "", err
	}
	if !validateSymmetricKey(buf) {
		return "", fmt.Errorf("error in generateRandomID: crypto/rand failed to generate random data")
	}
	id = common.Bytes2Hex(buf)
	return id, err
}
