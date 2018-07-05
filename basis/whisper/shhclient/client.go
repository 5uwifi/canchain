//
// (at your option) any later version.
//
//

package shhclient

import (
	"context"

	"github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/rpc"
	whisper "github.com/5uwifi/canchain/basis/whisper/whisperv6"
)

type Client struct {
	c *rpc.Client
}

func Dial(rawurl string) (*Client, error) {
	c, err := rpc.Dial(rawurl)
	if err != nil {
		return nil, err
	}
	return NewClient(c), nil
}

func NewClient(c *rpc.Client) *Client {
	return &Client{c}
}

func (sc *Client) Version(ctx context.Context) (string, error) {
	var result string
	err := sc.c.CallContext(ctx, &result, "shh_version")
	return result, err
}

func (sc *Client) Info(ctx context.Context) (whisper.Info, error) {
	var info whisper.Info
	err := sc.c.CallContext(ctx, &info, "shh_info")
	return info, err
}

func (sc *Client) SetMaxMessageSize(ctx context.Context, size uint32) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_setMaxMessageSize", size)
}

func (sc *Client) SetMinimumPoW(ctx context.Context, pow float64) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_setMinPoW", pow)
}

func (sc *Client) MarkTrustedPeer(ctx context.Context, ccnode string) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_markTrustedPeer", ccnode)
}

func (sc *Client) NewKeyPair(ctx context.Context) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_newKeyPair")
}

func (sc *Client) AddPrivateKey(ctx context.Context, key []byte) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_addPrivateKey", hexutil.Bytes(key))
}

func (sc *Client) DeleteKeyPair(ctx context.Context, id string) (string, error) {
	var ignored bool
	return id, sc.c.CallContext(ctx, &ignored, "shh_deleteKeyPair", id)
}

func (sc *Client) HasKeyPair(ctx context.Context, id string) (bool, error) {
	var has bool
	return has, sc.c.CallContext(ctx, &has, "shh_hasKeyPair", id)
}

func (sc *Client) PublicKey(ctx context.Context, id string) ([]byte, error) {
	var key hexutil.Bytes
	return []byte(key), sc.c.CallContext(ctx, &key, "shh_getPublicKey", id)
}

func (sc *Client) PrivateKey(ctx context.Context, id string) ([]byte, error) {
	var key hexutil.Bytes
	return []byte(key), sc.c.CallContext(ctx, &key, "shh_getPrivateKey", id)
}

func (sc *Client) NewSymmetricKey(ctx context.Context) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_newSymKey")
}

func (sc *Client) AddSymmetricKey(ctx context.Context, key []byte) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_addSymKey", hexutil.Bytes(key))
}

func (sc *Client) GenerateSymmetricKeyFromPassword(ctx context.Context, passwd string) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_generateSymKeyFromPassword", passwd)
}

func (sc *Client) HasSymmetricKey(ctx context.Context, id string) (bool, error) {
	var found bool
	return found, sc.c.CallContext(ctx, &found, "shh_hasSymKey", id)
}

func (sc *Client) GetSymmetricKey(ctx context.Context, id string) ([]byte, error) {
	var key hexutil.Bytes
	return []byte(key), sc.c.CallContext(ctx, &key, "shh_getSymKey", id)
}

func (sc *Client) DeleteSymmetricKey(ctx context.Context, id string) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_deleteSymKey", id)
}

func (sc *Client) Post(ctx context.Context, message whisper.NewMessage) (string, error) {
	var hash string
	return hash, sc.c.CallContext(ctx, &hash, "shh_post", message)
}

func (sc *Client) SubscribeMessages(ctx context.Context, criteria whisper.Criteria, ch chan<- *whisper.Message) (canchain.Subscription, error) {
	return sc.c.ShhSubscribe(ctx, ch, "messages", criteria)
}

func (sc *Client) NewMessageFilter(ctx context.Context, criteria whisper.Criteria) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_newMessageFilter", criteria)
}

func (sc *Client) DeleteMessageFilter(ctx context.Context, id string) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_deleteMessageFilter", id)
}

func (sc *Client) FilterMessages(ctx context.Context, id string) ([]*whisper.Message, error) {
	var messages []*whisper.Message
	return messages, sc.c.CallContext(ctx, &messages, "shh_getFilterMessages", id)
}
