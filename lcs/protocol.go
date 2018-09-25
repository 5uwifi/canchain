package lcs

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/rlp"
)

const (
	lpv1 = 1
	lpv2 = 2
)

var (
	ClientProtocolVersions    = []uint{lpv2, lpv1}
	ServerProtocolVersions    = []uint{lpv2, lpv1}
	AdvertiseProtocolVersions = []uint{lpv2}
)

var ProtocolLengths = map[uint]uint64{lpv1: 15, lpv2: 22}

const (
	NetworkId          = 1
	ProtocolMaxMsgSize = 10 * 1024 * 1024
)

const (
	StatusMsg              = 0x00
	AnnounceMsg            = 0x01
	GetBlockHeadersMsg     = 0x02
	BlockHeadersMsg        = 0x03
	GetBlockBodiesMsg      = 0x04
	BlockBodiesMsg         = 0x05
	GetReceiptsMsg         = 0x06
	ReceiptsMsg            = 0x07
	GetProofsV1Msg         = 0x08
	ProofsV1Msg            = 0x09
	GetCodeMsg             = 0x0a
	CodeMsg                = 0x0b
	SendTxMsg              = 0x0c
	GetHeaderProofsMsg     = 0x0d
	HeaderProofsMsg        = 0x0e
	GetProofsV2Msg         = 0x0f
	ProofsV2Msg            = 0x10
	GetHelperTrieProofsMsg = 0x11
	HelperTrieProofsMsg    = 0x12
	SendTxV2Msg            = 0x13
	GetTxStatusMsg         = 0x14
	TxStatusMsg            = 0x15
)

type errCode int

const (
	ErrMsgTooLarge = iota
	ErrDecode
	ErrInvalidMsgCode
	ErrProtocolVersionMismatch
	ErrNetworkIdMismatch
	ErrGenesisBlockMismatch
	ErrNoStatusMsg
	ErrExtraStatusMsg
	ErrSuspendedPeer
	ErrUselessPeer
	ErrRequestRejected
	ErrUnexpectedResponse
	ErrInvalidResponse
	ErrTooManyTimeouts
	ErrMissingKey
)

func (e errCode) String() string {
	return errorToString[int(e)]
}

var errorToString = map[int]string{
	ErrMsgTooLarge:             "Message too long",
	ErrDecode:                  "Invalid message",
	ErrInvalidMsgCode:          "Invalid message code",
	ErrProtocolVersionMismatch: "Protocol version mismatch",
	ErrNetworkIdMismatch:       "NetworkId mismatch",
	ErrGenesisBlockMismatch:    "Genesis block mismatch",
	ErrNoStatusMsg:             "No status message",
	ErrExtraStatusMsg:          "Extra status message",
	ErrSuspendedPeer:           "Suspended peer",
	ErrRequestRejected:         "Request rejected",
	ErrUnexpectedResponse:      "Unexpected response",
	ErrInvalidResponse:         "Invalid response",
	ErrTooManyTimeouts:         "Too many request timeouts",
	ErrMissingKey:              "Key missing from list",
}

type announceBlock struct {
	Hash   common.Hash
	Number uint64
	Td     *big.Int
}

type announceData struct {
	Hash       common.Hash
	Number     uint64
	Td         *big.Int
	ReorgDepth uint64
	Update     keyValueList
}

func (a *announceData) sign(privKey *ecdsa.PrivateKey) {
	rlp, _ := rlp.EncodeToBytes(announceBlock{a.Hash, a.Number, a.Td})
	sig, _ := crypto.Sign(crypto.Keccak256(rlp), privKey)
	a.Update = a.Update.add("sign", sig)
}

func (a *announceData) checkSignature(id cnode.ID) error {
	var sig []byte
	if err := a.Update.decode().get("sign", &sig); err != nil {
		return err
	}
	rlp, _ := rlp.EncodeToBytes(announceBlock{a.Hash, a.Number, a.Td})
	recPubkey, err := crypto.SigToPub(crypto.Keccak256(rlp), sig)
	if err != nil {
		return err
	}
	if id == cnode.PubkeyToIDV4(recPubkey) {
		return nil
	}
	return errors.New("wrong signature")
}

type blockInfo struct {
	Hash   common.Hash
	Number uint64
	Td     *big.Int
}

type getBlockHeadersData struct {
	Origin  hashOrNumber
	Amount  uint64
	Skip    uint64
	Reverse bool
}

type hashOrNumber struct {
	Hash   common.Hash
	Number uint64
}

func (hn *hashOrNumber) EncodeRLP(w io.Writer) error {
	if hn.Hash == (common.Hash{}) {
		return rlp.Encode(w, hn.Number)
	}
	if hn.Number != 0 {
		return fmt.Errorf("both origin hash (%x) and number (%d) provided", hn.Hash, hn.Number)
	}
	return rlp.Encode(w, hn.Hash)
}

func (hn *hashOrNumber) DecodeRLP(s *rlp.Stream) error {
	_, size, _ := s.Kind()
	origin, err := s.Raw()
	if err == nil {
		switch {
		case size == 32:
			err = rlp.DecodeBytes(origin, &hn.Hash)
		case size <= 8:
			err = rlp.DecodeBytes(origin, &hn.Number)
		default:
			err = fmt.Errorf("invalid input size %d for origin", size)
		}
	}
	return err
}

type CodeData []struct {
	Value []byte
}

type proofsData [][]rlp.RawValue

type txStatus struct {
	Status kernel.TxStatus
	Lookup *rawdb.TxLookupEntry `rlp:"nil"`
	Error  string
}
