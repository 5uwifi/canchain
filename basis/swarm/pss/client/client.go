//
// (at your option) any later version.
//
//

// +build !noclient,!noprotocol

package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/basis/p2p/discover"
	"github.com/5uwifi/canchain/basis/p2p/protocols"
	"github.com/5uwifi/canchain/basis/rlp"
	"github.com/5uwifi/canchain/rpc"
	"github.com/5uwifi/canchain/basis/swarm/log"
	"github.com/5uwifi/canchain/basis/swarm/pss"
)

const (
	handshakeRetryTimeout = 1000
	handshakeRetryCount   = 3
)

type Client struct {
	BaseAddrHex string

	// peers
	peerPool map[pss.Topic]map[string]*pssRPCRW
	protos   map[pss.Topic]*p2p.Protocol

	// rpc connections
	rpc  *rpc.Client
	subs []*rpc.ClientSubscription

	// channels
	topicsC chan []byte
	quitC   chan struct{}

	poolMu sync.Mutex
}

type pssRPCRW struct {
	*Client
	topic    string
	msgC     chan []byte
	addr     pss.PssAddress
	pubKeyId string
	lastSeen time.Time
	closed   bool
}

func (c *Client) newpssRPCRW(pubkeyid string, addr pss.PssAddress, topicobj pss.Topic) (*pssRPCRW, error) {
	topic := topicobj.String()
	err := c.rpc.Call(nil, "pss_setPeerPublicKey", pubkeyid, topic, hexutil.Encode(addr[:]))
	if err != nil {
		return nil, fmt.Errorf("setpeer %s %s: %v", topic, pubkeyid, err)
	}
	return &pssRPCRW{
		Client:   c,
		topic:    topic,
		msgC:     make(chan []byte),
		addr:     addr,
		pubKeyId: pubkeyid,
	}, nil
}

func (rw *pssRPCRW) ReadMsg() (p2p.Msg, error) {
	msg := <-rw.msgC
	log.Trace("pssrpcrw read", "msg", msg)
	pmsg, err := pss.ToP2pMsg(msg)
	if err != nil {
		return p2p.Msg{}, err
	}

	return pmsg, nil
}

// - any api calls fail
// - handshake retries are exhausted without reply,
// - send fails
func (rw *pssRPCRW) WriteMsg(msg p2p.Msg) error {
	log.Trace("got writemsg pssclient", "msg", msg)
	if rw.closed {
		return fmt.Errorf("connection closed")
	}
	rlpdata := make([]byte, msg.Size)
	msg.Payload.Read(rlpdata)
	pmsg, err := rlp.EncodeToBytes(pss.ProtocolMsg{
		Code:    msg.Code,
		Size:    msg.Size,
		Payload: rlpdata,
	})
	if err != nil {
		return err
	}

	// Get the keys we have
	var symkeyids []string
	err = rw.Client.rpc.Call(&symkeyids, "pss_getHandshakeKeys", rw.pubKeyId, rw.topic, false, true)
	if err != nil {
		return err
	}

	// Check the capacity of the first key
	var symkeycap uint16
	if len(symkeyids) > 0 {
		err = rw.Client.rpc.Call(&symkeycap, "pss_getHandshakeKeyCapacity", symkeyids[0])
		if err != nil {
			return err
		}
	}

	err = rw.Client.rpc.Call(nil, "pss_sendSym", symkeyids[0], rw.topic, hexutil.Encode(pmsg))
	if err != nil {
		return err
	}

	// If this is the last message it is valid for, initiate new handshake
	if symkeycap == 1 {
		var retries int
		var sync bool
		// if it's the only remaining key, make sure we don't continue until we have new ones for further writes
		if len(symkeyids) == 1 {
			sync = true
		}
		// initiate handshake
		_, err := rw.handshake(retries, sync, false)
		if err != nil {
			log.Warn("failing", "err", err)
			return err
		}
	}
	return nil
}

func (rw *pssRPCRW) handshake(retries int, sync bool, flush bool) (string, error) {

	var symkeyids []string
	var i int
	// request new keys
	// if the key buffer was depleted, make this as a blocking call and try several times before giving up
	for i = 0; i < 1+retries; i++ {
		log.Debug("handshake attempt pssrpcrw", "pubkeyid", rw.pubKeyId, "topic", rw.topic, "sync", sync)
		err := rw.Client.rpc.Call(&symkeyids, "pss_handshake", rw.pubKeyId, rw.topic, sync, flush)
		if err == nil {
			var keyid string
			if sync {
				keyid = symkeyids[0]
			}
			return keyid, nil
		}
		if i-1+retries > 1 {
			time.Sleep(time.Millisecond * handshakeRetryTimeout)
		}
	}

	return "", fmt.Errorf("handshake failed after %d attempts", i)
}

//
func NewClient(rpcurl string) (*Client, error) {
	rpcclient, err := rpc.Dial(rpcurl)
	if err != nil {
		return nil, err
	}

	client, err := NewClientWithRPC(rpcclient)
	if err != nil {
		return nil, err
	}
	return client, nil
}

//
func NewClientWithRPC(rpcclient *rpc.Client) (*Client, error) {
	client := newClient()
	client.rpc = rpcclient
	err := client.rpc.Call(&client.BaseAddrHex, "pss_baseAddr")
	if err != nil {
		return nil, fmt.Errorf("cannot get pss node baseaddress: %v", err)
	}
	return client, nil
}

func newClient() (client *Client) {
	client = &Client{
		quitC:    make(chan struct{}),
		peerPool: make(map[pss.Topic]map[string]*pssRPCRW),
		protos:   make(map[pss.Topic]*p2p.Protocol),
	}
	return
}

//
//
func (c *Client) RunProtocol(ctx context.Context, proto *p2p.Protocol) error {
	topicobj := pss.BytesToTopic([]byte(fmt.Sprintf("%s:%d", proto.Name, proto.Version)))
	topichex := topicobj.String()
	msgC := make(chan pss.APIMsg)
	c.peerPool[topicobj] = make(map[string]*pssRPCRW)
	sub, err := c.rpc.Subscribe(ctx, "pss", msgC, "receive", topichex)
	if err != nil {
		return fmt.Errorf("pss event subscription failed: %v", err)
	}
	c.subs = append(c.subs, sub)
	err = c.rpc.Call(nil, "pss_addHandshake", topichex)
	if err != nil {
		return fmt.Errorf("pss handshake activation failed: %v", err)
	}

	// dispatch incoming messages
	go func() {
		for {
			select {
			case msg := <-msgC:
				// we only allow sym msgs here
				if msg.Asymmetric {
					continue
				}
				// we get passed the symkeyid
				// need the symkey itself to resolve to peer's pubkey
				var pubkeyid string
				err = c.rpc.Call(&pubkeyid, "pss_getHandshakePublicKey", msg.Key)
				if err != nil || pubkeyid == "" {
					log.Trace("proto err or no pubkey", "err", err, "symkeyid", msg.Key)
					continue
				}
				// if we don't have the peer on this protocol already, create it
				// this is more or less the same as AddPssPeer, less the handshake initiation
				if c.peerPool[topicobj][pubkeyid] == nil {
					var addrhex string
					err := c.rpc.Call(&addrhex, "pss_getAddress", topichex, false, msg.Key)
					if err != nil {
						log.Trace(err.Error())
						continue
					}
					addrbytes, err := hexutil.Decode(addrhex)
					if err != nil {
						log.Trace(err.Error())
						break
					}
					addr := pss.PssAddress(addrbytes)
					rw, err := c.newpssRPCRW(pubkeyid, addr, topicobj)
					if err != nil {
						break
					}
					c.peerPool[topicobj][pubkeyid] = rw
					nid, _ := discover.HexID("0x00")
					p := p2p.NewPeer(nid, fmt.Sprintf("%v", addr), []p2p.Cap{})
					go proto.Run(p, c.peerPool[topicobj][pubkeyid])
				}
				go func() {
					c.peerPool[topicobj][pubkeyid].msgC <- msg.Msg
				}()
			case <-c.quitC:
				return
			}
		}
	}()

	c.protos[topicobj] = proto
	return nil
}

func (c *Client) Close() error {
	for _, s := range c.subs {
		s.Unsubscribe()
	}
	return nil
}

//
//
func (c *Client) AddPssPeer(pubkeyid string, addr []byte, spec *protocols.Spec) error {
	topic := pss.ProtocolTopic(spec)
	if c.peerPool[topic] == nil {
		return errors.New("addpeer on unset topic")
	}
	if c.peerPool[topic][pubkeyid] == nil {
		rw, err := c.newpssRPCRW(pubkeyid, addr, topic)
		if err != nil {
			return err
		}
		_, err = rw.handshake(handshakeRetryCount, true, true)
		if err != nil {
			return err
		}
		c.poolMu.Lock()
		c.peerPool[topic][pubkeyid] = rw
		c.poolMu.Unlock()
		nid, _ := discover.HexID("0x00")
		p := p2p.NewPeer(nid, fmt.Sprintf("%v", addr), []p2p.Cap{})
		go c.protos[topic].Run(p, c.peerPool[topic][pubkeyid])
	}
	return nil
}

//
func (c *Client) RemovePssPeer(pubkeyid string, spec *protocols.Spec) {
	log.Debug("closing pss client peer", "pubkey", pubkeyid, "protoname", spec.Name, "protoversion", spec.Version)
	c.poolMu.Lock()
	defer c.poolMu.Unlock()
	topic := pss.ProtocolTopic(spec)
	c.peerPool[topic][pubkeyid].closed = true
	delete(c.peerPool[topic], pubkeyid)
}
