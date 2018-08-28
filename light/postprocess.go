package light

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/bitutil"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/rlp"
	"github.com/5uwifi/canchain/lib/trie"
	"github.com/5uwifi/canchain/params"
)

const (
	CHTFrequencyClient = 32768

	CHTFrequencyServer = 4096

	HelperTrieConfirmations        = 2048
	HelperTrieProcessConfirmations = 256
)

type TrustedCheckpoint struct {
	name                            string
	SectionIdx                      uint64
	SectionHead, CHTRoot, BloomRoot common.Hash
}

var trustedCheckpoints = map[common.Hash]TrustedCheckpoint{
	params.MainnetGenesisHash: {
		name:        "mainnet",
		SectionIdx:  187,
		SectionHead: common.HexToHash("e6baa034efa31562d71ff23676512dec6562c1ad0301e08843b907e81958c696"),
		CHTRoot:     common.HexToHash("28001955219719cf06de1b08648969139d123a9835fc760547a1e4dabdabc15a"),
		BloomRoot:   common.HexToHash("395ca2373fc662720ac6b58b3bbe71f68aa0f38b63b2d3553dd32ff3c51eebc4"),
	},
	params.TestnetGenesisHash: {
		name:        "ropsten",
		SectionIdx:  117,
		SectionHead: common.HexToHash("9529b38631ae30783f56cbe4c3b9f07575b770ecba4f6e20a274b1e2f40fede1"),
		CHTRoot:     common.HexToHash("6f48e9f101f1fac98e7d74fbbcc4fda138358271ffd974d40d2506f0308bb363"),
		BloomRoot:   common.HexToHash("8242342e66e942c0cd893484e6736b9862ceb88b43ca344bb06a8285ac1b6d64"),
	},
	params.IronmanGenesisHash: {
		name:        "rinkeby",
		SectionIdx:  85,
		SectionHead: common.HexToHash("92cfa67afc4ad8ab0dcbc6fa49efd14b5b19402442e7317e6bc879d85f89d64d"),
		CHTRoot:     common.HexToHash("2802ec92cd7a54a75bca96afdc666ae7b99e5d96cf8192dcfb09588812f51564"),
		BloomRoot:   common.HexToHash("ebefeb31a9a42866d8cf2d2477704b4c3d7c20d0e4e9b5aaa77f396e016a1263"),
	},
}

var (
	ErrNoTrustedCht       = errors.New("No trusted canonical hash trie")
	ErrNoTrustedBloomTrie = errors.New("No trusted bloom trie")
	ErrNoHeader           = errors.New("Header not found")
	chtPrefix             = []byte("chtRoot-")
	ChtTablePrefix        = "cht-"
)

type ChtNode struct {
	Hash common.Hash
	Td   *big.Int
}

func GetChtRoot(db candb.Database, sectionIdx uint64, sectionHead common.Hash) common.Hash {
	var encNumber [8]byte
	binary.BigEndian.PutUint64(encNumber[:], sectionIdx)
	data, _ := db.Get(append(append(chtPrefix, encNumber[:]...), sectionHead.Bytes()...))
	return common.BytesToHash(data)
}

func GetChtV2Root(db candb.Database, sectionIdx uint64, sectionHead common.Hash) common.Hash {
	return GetChtRoot(db, (sectionIdx+1)*(CHTFrequencyClient/CHTFrequencyServer)-1, sectionHead)
}

func StoreChtRoot(db candb.Database, sectionIdx uint64, sectionHead, root common.Hash) {
	var encNumber [8]byte
	binary.BigEndian.PutUint64(encNumber[:], sectionIdx)
	db.Put(append(append(chtPrefix, encNumber[:]...), sectionHead.Bytes()...), root.Bytes())
}

type ChtIndexerBackend struct {
	diskdb, trieTable    candb.Database
	odr                  OdrBackend
	triedb               *trie.Database
	section, sectionSize uint64
	lastHash             common.Hash
	trie                 *trie.Trie
}

func NewChtIndexer(db candb.Database, clientMode bool, odr OdrBackend) *kernel.ChainIndexer {
	var sectionSize, confirmReq uint64
	if clientMode {
		sectionSize = CHTFrequencyClient
		confirmReq = HelperTrieConfirmations
	} else {
		sectionSize = CHTFrequencyServer
		confirmReq = HelperTrieProcessConfirmations
	}
	idb := candb.NewTable(db, "chtIndex-")
	trieTable := candb.NewTable(db, ChtTablePrefix)
	backend := &ChtIndexerBackend{
		diskdb:      db,
		odr:         odr,
		trieTable:   trieTable,
		triedb:      trie.NewDatabase(trieTable),
		sectionSize: sectionSize,
	}
	return kernel.NewChainIndexer(db, idb, backend, sectionSize, confirmReq, time.Millisecond*100, "cht")
}

func (c *ChtIndexerBackend) fetchMissingNodes(ctx context.Context, section uint64, root common.Hash) error {
	batch := c.trieTable.NewBatch()
	r := &ChtRequest{ChtRoot: root, ChtNum: section - 1, BlockNum: section*c.sectionSize - 1}
	for {
		err := c.odr.Retrieve(ctx, r)
		switch err {
		case nil:
			r.Proof.Store(batch)
			return batch.Write()
		case ErrNoPeers:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second * 10):
			}
		default:
			return err
		}
	}
}

func (c *ChtIndexerBackend) Reset(ctx context.Context, section uint64, lastSectionHead common.Hash) error {
	var root common.Hash
	if section > 0 {
		root = GetChtRoot(c.diskdb, section-1, lastSectionHead)
	}
	var err error
	c.trie, err = trie.New(root, c.triedb)

	if err != nil && c.odr != nil {
		err = c.fetchMissingNodes(ctx, section, root)
		if err == nil {
			c.trie, err = trie.New(root, c.triedb)
		}
	}

	c.section = section
	return err
}

func (c *ChtIndexerBackend) Process(ctx context.Context, header *types.Header) error {
	hash, num := header.Hash(), header.Number.Uint64()
	c.lastHash = hash

	td := rawdb.ReadTd(c.diskdb, hash, num)
	if td == nil {
		panic(nil)
	}
	var encNumber [8]byte
	binary.BigEndian.PutUint64(encNumber[:], num)
	data, _ := rlp.EncodeToBytes(ChtNode{hash, td})
	c.trie.Update(encNumber[:], data)
	return nil
}

func (c *ChtIndexerBackend) Commit() error {
	root, err := c.trie.Commit(nil)
	if err != nil {
		return err
	}
	c.triedb.Commit(root, false)

	if ((c.section+1)*c.sectionSize)%CHTFrequencyClient == 0 {
		log4j.Info("Storing CHT", "section", c.section*c.sectionSize/CHTFrequencyClient, "head", fmt.Sprintf("%064x", c.lastHash), "root", fmt.Sprintf("%064x", root))
	}
	StoreChtRoot(c.diskdb, c.section, c.lastHash, root)
	return nil
}

const (
	BloomTrieFrequency  = 32768
	ethBloomBitsSection = 4096
)

var (
	bloomTriePrefix      = []byte("bltRoot-")
	BloomTrieTablePrefix = "blt-"
)

func GetBloomTrieRoot(db candb.Database, sectionIdx uint64, sectionHead common.Hash) common.Hash {
	var encNumber [8]byte
	binary.BigEndian.PutUint64(encNumber[:], sectionIdx)
	data, _ := db.Get(append(append(bloomTriePrefix, encNumber[:]...), sectionHead.Bytes()...))
	return common.BytesToHash(data)
}

func StoreBloomTrieRoot(db candb.Database, sectionIdx uint64, sectionHead, root common.Hash) {
	var encNumber [8]byte
	binary.BigEndian.PutUint64(encNumber[:], sectionIdx)
	db.Put(append(append(bloomTriePrefix, encNumber[:]...), sectionHead.Bytes()...), root.Bytes())
}

type BloomTrieIndexerBackend struct {
	diskdb, trieTable                          candb.Database
	odr                                        OdrBackend
	triedb                                     *trie.Database
	section, parentSectionSize, bloomTrieRatio uint64
	trie                                       *trie.Trie
	sectionHeads                               []common.Hash
}

func NewBloomTrieIndexer(db candb.Database, clientMode bool, odr OdrBackend) *kernel.ChainIndexer {
	trieTable := candb.NewTable(db, BloomTrieTablePrefix)
	backend := &BloomTrieIndexerBackend{
		diskdb:    db,
		odr:       odr,
		trieTable: trieTable,
		triedb:    trie.NewDatabase(trieTable),
	}
	idb := candb.NewTable(db, "bltIndex-")

	if clientMode {
		backend.parentSectionSize = BloomTrieFrequency
	} else {
		backend.parentSectionSize = ethBloomBitsSection
	}
	backend.bloomTrieRatio = BloomTrieFrequency / backend.parentSectionSize
	backend.sectionHeads = make([]common.Hash, backend.bloomTrieRatio)
	return kernel.NewChainIndexer(db, idb, backend, BloomTrieFrequency, 0, time.Millisecond*100, "bloomtrie")
}

func (b *BloomTrieIndexerBackend) fetchMissingNodes(ctx context.Context, section uint64, root common.Hash) error {
	indexCh := make(chan uint, types.BloomBitLength)
	type res struct {
		nodes *NodeSet
		err   error
	}
	resCh := make(chan res, types.BloomBitLength)
	for i := 0; i < 20; i++ {
		go func() {
			for bitIndex := range indexCh {
				r := &BloomRequest{BloomTrieRoot: root, BloomTrieNum: section - 1, BitIdx: bitIndex, SectionIdxList: []uint64{section - 1}}
				for {
					if err := b.odr.Retrieve(ctx, r); err == ErrNoPeers {
						select {
						case <-ctx.Done():
							resCh <- res{nil, ctx.Err()}
							return
						case <-time.After(time.Second * 10):
						}
					} else {
						resCh <- res{r.Proofs, err}
						break
					}
				}
			}
		}()
	}

	for i := uint(0); i < types.BloomBitLength; i++ {
		indexCh <- i
	}
	close(indexCh)
	batch := b.trieTable.NewBatch()
	for i := uint(0); i < types.BloomBitLength; i++ {
		res := <-resCh
		if res.err != nil {
			return res.err
		}
		res.nodes.Store(batch)
	}
	return batch.Write()
}

func (b *BloomTrieIndexerBackend) Reset(ctx context.Context, section uint64, lastSectionHead common.Hash) error {
	var root common.Hash
	if section > 0 {
		root = GetBloomTrieRoot(b.diskdb, section-1, lastSectionHead)
	}
	var err error
	b.trie, err = trie.New(root, b.triedb)
	if err != nil && b.odr != nil {
		err = b.fetchMissingNodes(ctx, section, root)
		if err == nil {
			b.trie, err = trie.New(root, b.triedb)
		}
	}
	b.section = section
	return err
}

func (b *BloomTrieIndexerBackend) Process(ctx context.Context, header *types.Header) error {
	num := header.Number.Uint64() - b.section*BloomTrieFrequency
	if (num+1)%b.parentSectionSize == 0 {
		b.sectionHeads[num/b.parentSectionSize] = header.Hash()
	}
	return nil
}

func (b *BloomTrieIndexerBackend) Commit() error {
	var compSize, decompSize uint64

	for i := uint(0); i < types.BloomBitLength; i++ {
		var encKey [10]byte
		binary.BigEndian.PutUint16(encKey[0:2], uint16(i))
		binary.BigEndian.PutUint64(encKey[2:10], b.section)
		var decomp []byte
		for j := uint64(0); j < b.bloomTrieRatio; j++ {
			data, err := rawdb.ReadBloomBits(b.diskdb, i, b.section*b.bloomTrieRatio+j, b.sectionHeads[j])
			if err != nil {
				return err
			}
			decompData, err2 := bitutil.DecompressBytes(data, int(b.parentSectionSize/8))
			if err2 != nil {
				return err2
			}
			decomp = append(decomp, decompData...)
		}
		comp := bitutil.CompressBytes(decomp)

		decompSize += uint64(len(decomp))
		compSize += uint64(len(comp))
		if len(comp) > 0 {
			b.trie.Update(encKey[:], comp)
		} else {
			b.trie.Delete(encKey[:])
		}
	}
	root, err := b.trie.Commit(nil)
	if err != nil {
		return err
	}
	b.triedb.Commit(root, false)

	sectionHead := b.sectionHeads[b.bloomTrieRatio-1]
	log4j.Info("Storing bloom trie", "section", b.section, "head", fmt.Sprintf("%064x", sectionHead), "root", fmt.Sprintf("%064x", root), "compression", float64(compSize)/float64(decompSize))
	StoreBloomTrieRoot(b.diskdb, b.section, sectionHead, root)

	return nil
}
