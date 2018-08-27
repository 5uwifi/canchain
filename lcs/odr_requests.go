package lcs

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/lib/trie"
	"github.com/5uwifi/canchain/light"
)

var (
	errInvalidMessageType  = errors.New("invalid message type")
	errInvalidEntryCount   = errors.New("invalid number of response entries")
	errHeaderUnavailable   = errors.New("header unavailable")
	errTxHashMismatch      = errors.New("transaction hash mismatch")
	errUncleHashMismatch   = errors.New("uncle hash mismatch")
	errReceiptHashMismatch = errors.New("receipt hash mismatch")
	errDataHashMismatch    = errors.New("data hash mismatch")
	errCHTHashMismatch     = errors.New("cht hash mismatch")
	errCHTNumberMismatch   = errors.New("cht number mismatch")
	errUselessNodes        = errors.New("useless nodes in merkle proof nodeset")
)

type LesOdrRequest interface {
	GetCost(*peer) uint64
	CanSend(*peer) bool
	Request(uint64, *peer) error
	Validate(candb.Database, *Msg) error
}

func LesRequest(req light.OdrRequest) LesOdrRequest {
	switch r := req.(type) {
	case *light.BlockRequest:
		return (*BlockRequest)(r)
	case *light.ReceiptsRequest:
		return (*ReceiptsRequest)(r)
	case *light.TrieRequest:
		return (*TrieRequest)(r)
	case *light.CodeRequest:
		return (*CodeRequest)(r)
	case *light.ChtRequest:
		return (*ChtRequest)(r)
	case *light.BloomRequest:
		return (*BloomRequest)(r)
	default:
		return nil
	}
}

type BlockRequest light.BlockRequest

func (r *BlockRequest) GetCost(peer *peer) uint64 {
	return peer.GetRequestCost(GetBlockBodiesMsg, 1)
}

func (r *BlockRequest) CanSend(peer *peer) bool {
	return peer.HasBlock(r.Hash, r.Number)
}

func (r *BlockRequest) Request(reqID uint64, peer *peer) error {
	peer.Log().Debug("Requesting block body", "hash", r.Hash)
	return peer.RequestBodies(reqID, r.GetCost(peer), []common.Hash{r.Hash})
}

func (r *BlockRequest) Validate(db candb.Database, msg *Msg) error {
	log4j.Debug("Validating block body", "hash", r.Hash)

	if msg.MsgType != MsgBlockBodies {
		return errInvalidMessageType
	}
	bodies := msg.Obj.([]*types.Body)
	if len(bodies) != 1 {
		return errInvalidEntryCount
	}
	body := bodies[0]

	header := rawdb.ReadHeader(db, r.Hash, r.Number)
	if header == nil {
		return errHeaderUnavailable
	}
	if header.TxHash != types.DeriveSha(types.Transactions(body.Transactions)) {
		return errTxHashMismatch
	}
	if header.UncleHash != types.CalcUncleHash(body.Uncles) {
		return errUncleHashMismatch
	}
	data, err := rlp.EncodeToBytes(body)
	if err != nil {
		return err
	}
	r.Rlp = data
	return nil
}

type ReceiptsRequest light.ReceiptsRequest

func (r *ReceiptsRequest) GetCost(peer *peer) uint64 {
	return peer.GetRequestCost(GetReceiptsMsg, 1)
}

func (r *ReceiptsRequest) CanSend(peer *peer) bool {
	return peer.HasBlock(r.Hash, r.Number)
}

func (r *ReceiptsRequest) Request(reqID uint64, peer *peer) error {
	peer.Log().Debug("Requesting block receipts", "hash", r.Hash)
	return peer.RequestReceipts(reqID, r.GetCost(peer), []common.Hash{r.Hash})
}

func (r *ReceiptsRequest) Validate(db candb.Database, msg *Msg) error {
	log4j.Debug("Validating block receipts", "hash", r.Hash)

	if msg.MsgType != MsgReceipts {
		return errInvalidMessageType
	}
	receipts := msg.Obj.([]types.Receipts)
	if len(receipts) != 1 {
		return errInvalidEntryCount
	}
	receipt := receipts[0]

	header := rawdb.ReadHeader(db, r.Hash, r.Number)
	if header == nil {
		return errHeaderUnavailable
	}
	if header.ReceiptHash != types.DeriveSha(receipt) {
		return errReceiptHashMismatch
	}
	r.Receipts = receipt
	return nil
}

type ProofReq struct {
	BHash       common.Hash
	AccKey, Key []byte
	FromLevel   uint
}

type TrieRequest light.TrieRequest

func (r *TrieRequest) GetCost(peer *peer) uint64 {
	switch peer.version {
	case lpv1:
		return peer.GetRequestCost(GetProofsV1Msg, 1)
	case lpv2:
		return peer.GetRequestCost(GetProofsV2Msg, 1)
	default:
		panic(nil)
	}
}

func (r *TrieRequest) CanSend(peer *peer) bool {
	return peer.HasBlock(r.Id.BlockHash, r.Id.BlockNumber)
}

func (r *TrieRequest) Request(reqID uint64, peer *peer) error {
	peer.Log().Debug("Requesting trie proof", "root", r.Id.Root, "key", r.Key)
	req := ProofReq{
		BHash:  r.Id.BlockHash,
		AccKey: r.Id.AccKey,
		Key:    r.Key,
	}
	return peer.RequestProofs(reqID, r.GetCost(peer), []ProofReq{req})
}

func (r *TrieRequest) Validate(db candb.Database, msg *Msg) error {
	log4j.Debug("Validating trie proof", "root", r.Id.Root, "key", r.Key)

	switch msg.MsgType {
	case MsgProofsV1:
		proofs := msg.Obj.([]light.NodeList)
		if len(proofs) != 1 {
			return errInvalidEntryCount
		}
		nodeSet := proofs[0].NodeSet()
		if _, _, err := trie.VerifyProof(r.Id.Root, r.Key, nodeSet); err != nil {
			return fmt.Errorf("merkle proof verification failed: %v", err)
		}
		r.Proof = nodeSet
		return nil

	case MsgProofsV2:
		proofs := msg.Obj.(light.NodeList)
		nodeSet := proofs.NodeSet()
		reads := &readTraceDB{db: nodeSet}
		if _, _, err := trie.VerifyProof(r.Id.Root, r.Key, reads); err != nil {
			return fmt.Errorf("merkle proof verification failed: %v", err)
		}
		if len(reads.reads) != nodeSet.KeyCount() {
			return errUselessNodes
		}
		r.Proof = nodeSet
		return nil

	default:
		return errInvalidMessageType
	}
}

type CodeReq struct {
	BHash  common.Hash
	AccKey []byte
}

type CodeRequest light.CodeRequest

func (r *CodeRequest) GetCost(peer *peer) uint64 {
	return peer.GetRequestCost(GetCodeMsg, 1)
}

func (r *CodeRequest) CanSend(peer *peer) bool {
	return peer.HasBlock(r.Id.BlockHash, r.Id.BlockNumber)
}

func (r *CodeRequest) Request(reqID uint64, peer *peer) error {
	peer.Log().Debug("Requesting code data", "hash", r.Hash)
	req := CodeReq{
		BHash:  r.Id.BlockHash,
		AccKey: r.Id.AccKey,
	}
	return peer.RequestCode(reqID, r.GetCost(peer), []CodeReq{req})
}

func (r *CodeRequest) Validate(db candb.Database, msg *Msg) error {
	log4j.Debug("Validating code data", "hash", r.Hash)

	if msg.MsgType != MsgCode {
		return errInvalidMessageType
	}
	reply := msg.Obj.([][]byte)
	if len(reply) != 1 {
		return errInvalidEntryCount
	}
	data := reply[0]

	if hash := crypto.Keccak256Hash(data); r.Hash != hash {
		return errDataHashMismatch
	}
	r.Data = data
	return nil
}

const (
	htCanonical = iota
	htBloomBits

	auxRoot   = 1
	auxHeader = 2
)

type HelperTrieReq struct {
	Type              uint
	TrieIdx           uint64
	Key               []byte
	FromLevel, AuxReq uint
}

type HelperTrieResps struct {
	Proofs  light.NodeList
	AuxData [][]byte
}

type ChtReq struct {
	ChtNum, BlockNum uint64
	FromLevel        uint
}

type ChtResp struct {
	Header *types.Header
	Proof  []rlp.RawValue
}

type ChtRequest light.ChtRequest

func (r *ChtRequest) GetCost(peer *peer) uint64 {
	switch peer.version {
	case lpv1:
		return peer.GetRequestCost(GetHeaderProofsMsg, 1)
	case lpv2:
		return peer.GetRequestCost(GetHelperTrieProofsMsg, 1)
	default:
		panic(nil)
	}
}

func (r *ChtRequest) CanSend(peer *peer) bool {
	peer.lock.RLock()
	defer peer.lock.RUnlock()

	return peer.headInfo.Number >= light.HelperTrieConfirmations && r.ChtNum <= (peer.headInfo.Number-light.HelperTrieConfirmations)/light.CHTFrequencyClient
}

func (r *ChtRequest) Request(reqID uint64, peer *peer) error {
	peer.Log().Debug("Requesting CHT", "cht", r.ChtNum, "block", r.BlockNum)
	var encNum [8]byte
	binary.BigEndian.PutUint64(encNum[:], r.BlockNum)
	req := HelperTrieReq{
		Type:    htCanonical,
		TrieIdx: r.ChtNum,
		Key:     encNum[:],
		AuxReq:  auxHeader,
	}
	return peer.RequestHelperTrieProofs(reqID, r.GetCost(peer), []HelperTrieReq{req})
}

func (r *ChtRequest) Validate(db candb.Database, msg *Msg) error {
	log4j.Debug("Validating CHT", "cht", r.ChtNum, "block", r.BlockNum)

	switch msg.MsgType {
	case MsgHeaderProofs:
		proofs := msg.Obj.([]ChtResp)
		if len(proofs) != 1 {
			return errInvalidEntryCount
		}
		proof := proofs[0]

		var encNumber [8]byte
		binary.BigEndian.PutUint64(encNumber[:], r.BlockNum)

		value, _, err := trie.VerifyProof(r.ChtRoot, encNumber[:], light.NodeList(proof.Proof).NodeSet())
		if err != nil {
			return err
		}
		var node light.ChtNode
		if err := rlp.DecodeBytes(value, &node); err != nil {
			return err
		}
		if node.Hash != proof.Header.Hash() {
			return errCHTHashMismatch
		}
		r.Header = proof.Header
		r.Proof = light.NodeList(proof.Proof).NodeSet()
		r.Td = node.Td
	case MsgHelperTrieProofs:
		resp := msg.Obj.(HelperTrieResps)
		if len(resp.AuxData) != 1 {
			return errInvalidEntryCount
		}
		nodeSet := resp.Proofs.NodeSet()
		headerEnc := resp.AuxData[0]
		if len(headerEnc) == 0 {
			return errHeaderUnavailable
		}
		header := new(types.Header)
		if err := rlp.DecodeBytes(headerEnc, header); err != nil {
			return errHeaderUnavailable
		}

		var encNumber [8]byte
		binary.BigEndian.PutUint64(encNumber[:], r.BlockNum)

		reads := &readTraceDB{db: nodeSet}
		value, _, err := trie.VerifyProof(r.ChtRoot, encNumber[:], reads)
		if err != nil {
			return fmt.Errorf("merkle proof verification failed: %v", err)
		}
		if len(reads.reads) != nodeSet.KeyCount() {
			return errUselessNodes
		}

		var node light.ChtNode
		if err := rlp.DecodeBytes(value, &node); err != nil {
			return err
		}
		if node.Hash != header.Hash() {
			return errCHTHashMismatch
		}
		if r.BlockNum != header.Number.Uint64() {
			return errCHTNumberMismatch
		}
		r.Header = header
		r.Proof = nodeSet
		r.Td = node.Td
	default:
		return errInvalidMessageType
	}
	return nil
}

type BloomReq struct {
	BloomTrieNum, BitIdx, SectionIdx, FromLevel uint64
}

type BloomRequest light.BloomRequest

func (r *BloomRequest) GetCost(peer *peer) uint64 {
	return peer.GetRequestCost(GetHelperTrieProofsMsg, len(r.SectionIdxList))
}

func (r *BloomRequest) CanSend(peer *peer) bool {
	peer.lock.RLock()
	defer peer.lock.RUnlock()

	if peer.version < lpv2 {
		return false
	}
	return peer.headInfo.Number >= light.HelperTrieConfirmations && r.BloomTrieNum <= (peer.headInfo.Number-light.HelperTrieConfirmations)/light.BloomTrieFrequency
}

func (r *BloomRequest) Request(reqID uint64, peer *peer) error {
	peer.Log().Debug("Requesting BloomBits", "bloomTrie", r.BloomTrieNum, "bitIdx", r.BitIdx, "sections", r.SectionIdxList)
	reqs := make([]HelperTrieReq, len(r.SectionIdxList))

	var encNumber [10]byte
	binary.BigEndian.PutUint16(encNumber[:2], uint16(r.BitIdx))

	for i, sectionIdx := range r.SectionIdxList {
		binary.BigEndian.PutUint64(encNumber[2:], sectionIdx)
		reqs[i] = HelperTrieReq{
			Type:    htBloomBits,
			TrieIdx: r.BloomTrieNum,
			Key:     common.CopyBytes(encNumber[:]),
		}
	}
	return peer.RequestHelperTrieProofs(reqID, r.GetCost(peer), reqs)
}

func (r *BloomRequest) Validate(db candb.Database, msg *Msg) error {
	log4j.Debug("Validating BloomBits", "bloomTrie", r.BloomTrieNum, "bitIdx", r.BitIdx, "sections", r.SectionIdxList)

	if msg.MsgType != MsgHelperTrieProofs {
		return errInvalidMessageType
	}
	resps := msg.Obj.(HelperTrieResps)
	proofs := resps.Proofs
	nodeSet := proofs.NodeSet()
	reads := &readTraceDB{db: nodeSet}

	r.BloomBits = make([][]byte, len(r.SectionIdxList))

	var encNumber [10]byte
	binary.BigEndian.PutUint16(encNumber[:2], uint16(r.BitIdx))

	for i, idx := range r.SectionIdxList {
		binary.BigEndian.PutUint64(encNumber[2:], idx)
		value, _, err := trie.VerifyProof(r.BloomTrieRoot, encNumber[:], reads)
		if err != nil {
			return err
		}
		r.BloomBits[i] = value
	}

	if len(reads.reads) != nodeSet.KeyCount() {
		return errUselessNodes
	}
	r.Proofs = nodeSet
	return nil
}

type readTraceDB struct {
	db    trie.DatabaseReader
	reads map[string]struct{}
}

func (db *readTraceDB) Get(k []byte) ([]byte, error) {
	if db.reads == nil {
		db.reads = make(map[string]struct{})
	}
	db.reads[string(k)] = struct{}{}
	return db.db.Get(k)
}

func (db *readTraceDB) Has(key []byte) (bool, error) {
	_, err := db.Get(key)
	return err == nil, nil
}
