package light

import (
	"encoding/binary"
	"errors"
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

type trustedCheckpoint struct {
	name                                string
	sectionIdx                          uint64
	sectionHead, chtRoot, bloomTrieRoot common.Hash
}

var (
	mainnetCheckpoint = trustedCheckpoint{
		name:          "mainnet",
		sectionIdx:    179,
		sectionHead:   common.HexToHash("ae778e455492db1183e566fa0c67f954d256fdd08618f6d5a393b0e24576d0ea"),
		chtRoot:       common.HexToHash("646b338f9ca74d936225338916be53710ec84020b89946004a8605f04c817f16"),
		bloomTrieRoot: common.HexToHash("d0f978f5dbc86e5bf931d8dd5b2ecbebbda6dc78f8896af6a27b46a3ced0ac25"),
	}

	ropstenCheckpoint = trustedCheckpoint{
		name:          "ropsten",
		sectionIdx:    107,
		sectionHead:   common.HexToHash("e1988f95399debf45b873e065e5cd61b416ef2e2e5deec5a6f87c3127086e1ce"),
		chtRoot:       common.HexToHash("15cba18e4de0ab1e95e202625199ba30147aec8b0b70384b66ebea31ba6a18e0"),
		bloomTrieRoot: common.HexToHash("e00fa6389b2e597d9df52172cd8e936879eed0fca4fa59db99e2c8ed682562f2"),
	}
)

var trustedCheckpoints = map[common.Hash]trustedCheckpoint{
	params.MainnetGenesisHash: mainnetCheckpoint,
	params.TestnetGenesisHash: ropstenCheckpoint,
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
	diskdb               candb.Database
	triedb               *trie.Database
	section, sectionSize uint64
	lastHash             common.Hash
	trie                 *trie.Trie
}

func NewChtIndexer(db candb.Database, clientMode bool) *kernel.ChainIndexer {
	var sectionSize, confirmReq uint64
	if clientMode {
		sectionSize = CHTFrequencyClient
		confirmReq = HelperTrieConfirmations
	} else {
		sectionSize = CHTFrequencyServer
		confirmReq = HelperTrieProcessConfirmations
	}
	idb := candb.NewTable(db, "chtIndex-")
	backend := &ChtIndexerBackend{
		diskdb:      db,
		triedb:      trie.NewDatabase(candb.NewTable(db, ChtTablePrefix)),
		sectionSize: sectionSize,
	}
	return kernel.NewChainIndexer(db, idb, backend, sectionSize, confirmReq, time.Millisecond*100, "cht")
}

func (c *ChtIndexerBackend) Reset(section uint64, lastSectionHead common.Hash) error {
	var root common.Hash
	if section > 0 {
		root = GetChtRoot(c.diskdb, section-1, lastSectionHead)
	}
	var err error
	c.trie, err = trie.New(root, c.triedb)
	c.section = section
	return err
}

func (c *ChtIndexerBackend) Process(header *types.Header) {
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
}

func (c *ChtIndexerBackend) Commit() error {
	root, err := c.trie.Commit(nil)
	if err != nil {
		return err
	}
	c.triedb.Commit(root, false)

	if ((c.section+1)*c.sectionSize)%CHTFrequencyClient == 0 {
		log4j.Info("Storing CHT", "section", c.section*c.sectionSize/CHTFrequencyClient, "head", c.lastHash, "root", root)
	}
	StoreChtRoot(c.diskdb, c.section, c.lastHash, root)
	return nil
}

const (
	BloomTrieFrequency        = 32768
	ethBloomBitsSection       = 4096
	ethBloomBitsConfirmations = 256
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
	diskdb                                     candb.Database
	triedb                                     *trie.Database
	section, parentSectionSize, bloomTrieRatio uint64
	trie                                       *trie.Trie
	sectionHeads                               []common.Hash
}

func NewBloomTrieIndexer(db candb.Database, clientMode bool) *kernel.ChainIndexer {
	backend := &BloomTrieIndexerBackend{
		diskdb: db,
		triedb: trie.NewDatabase(candb.NewTable(db, BloomTrieTablePrefix)),
	}
	idb := candb.NewTable(db, "bltIndex-")

	var confirmReq uint64
	if clientMode {
		backend.parentSectionSize = BloomTrieFrequency
		confirmReq = HelperTrieConfirmations
	} else {
		backend.parentSectionSize = ethBloomBitsSection
		confirmReq = HelperTrieProcessConfirmations
	}
	backend.bloomTrieRatio = BloomTrieFrequency / backend.parentSectionSize
	backend.sectionHeads = make([]common.Hash, backend.bloomTrieRatio)
	return kernel.NewChainIndexer(db, idb, backend, BloomTrieFrequency, confirmReq-ethBloomBitsConfirmations, time.Millisecond*100, "bloomtrie")
}

func (b *BloomTrieIndexerBackend) Reset(section uint64, lastSectionHead common.Hash) error {
	var root common.Hash
	if section > 0 {
		root = GetBloomTrieRoot(b.diskdb, section-1, lastSectionHead)
	}
	var err error
	b.trie, err = trie.New(root, b.triedb)
	b.section = section
	return err
}

func (b *BloomTrieIndexerBackend) Process(header *types.Header) {
	num := header.Number.Uint64() - b.section*BloomTrieFrequency
	if (num+1)%b.parentSectionSize == 0 {
		b.sectionHeads[num/b.parentSectionSize] = header.Hash()
	}
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
	log4j.Info("Storing bloom trie", "section", b.section, "head", sectionHead, "root", root, "compression", float64(compSize)/float64(decompSize))
	StoreBloomTrieRoot(b.diskdb, b.section, sectionHead, root)

	return nil
}
