package light

import (
	"context"
	"errors"
	"math/big"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
)

var NoOdr = context.Background()

var ErrNoPeers = errors.New("no suitable peers available")

type OdrBackend interface {
	Database() candb.Database
	ChtIndexer() *kernel.ChainIndexer
	BloomTrieIndexer() *kernel.ChainIndexer
	BloomIndexer() *kernel.ChainIndexer
	Retrieve(ctx context.Context, req OdrRequest) error
}

type OdrRequest interface {
	StoreResult(db candb.Database)
}

type TrieID struct {
	BlockHash, Root common.Hash
	BlockNumber     uint64
	AccKey          []byte
}

func StateTrieID(header *types.Header) *TrieID {
	return &TrieID{
		BlockHash:   header.Hash(),
		BlockNumber: header.Number.Uint64(),
		AccKey:      nil,
		Root:        header.Root,
	}
}

func StorageTrieID(state *TrieID, addrHash, root common.Hash) *TrieID {
	return &TrieID{
		BlockHash:   state.BlockHash,
		BlockNumber: state.BlockNumber,
		AccKey:      addrHash[:],
		Root:        root,
	}
}

type TrieRequest struct {
	OdrRequest
	Id    *TrieID
	Key   []byte
	Proof *NodeSet
}

func (req *TrieRequest) StoreResult(db candb.Database) {
	req.Proof.Store(db)
}

type CodeRequest struct {
	OdrRequest
	Id   *TrieID
	Hash common.Hash
	Data []byte
}

func (req *CodeRequest) StoreResult(db candb.Database) {
	db.Put(req.Hash[:], req.Data)
}

type BlockRequest struct {
	OdrRequest
	Hash   common.Hash
	Number uint64
	Rlp    []byte
}

func (req *BlockRequest) StoreResult(db candb.Database) {
	rawdb.WriteBodyRLP(db, req.Hash, req.Number, req.Rlp)
}

type ReceiptsRequest struct {
	OdrRequest
	Hash     common.Hash
	Number   uint64
	Receipts types.Receipts
}

func (req *ReceiptsRequest) StoreResult(db candb.Database) {
	rawdb.WriteReceipts(db, req.Hash, req.Number, req.Receipts)
}

type ChtRequest struct {
	OdrRequest
	ChtNum, BlockNum uint64
	ChtRoot          common.Hash
	Header           *types.Header
	Td               *big.Int
	Proof            *NodeSet
}

func (req *ChtRequest) StoreResult(db candb.Database) {
	hash, num := req.Header.Hash(), req.Header.Number.Uint64()

	rawdb.WriteHeader(db, req.Header)
	rawdb.WriteTd(db, hash, num, req.Td)
	rawdb.WriteCanonicalHash(db, hash, num)
}

type BloomRequest struct {
	OdrRequest
	BloomTrieNum   uint64
	BitIdx         uint
	SectionIdxList []uint64
	BloomTrieRoot  common.Hash
	BloomBits      [][]byte
	Proofs         *NodeSet
}

func (req *BloomRequest) StoreResult(db candb.Database) {
	for i, sectionIdx := range req.SectionIdxList {
		sectionHead := rawdb.ReadCanonicalHash(db, (sectionIdx+1)*BloomTrieFrequency-1)
		rawdb.WriteBloomBits(db, req.BitIdx, sectionIdx, sectionHead, req.BloomBits[i])
	}
}
