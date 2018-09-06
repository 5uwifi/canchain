package can

import (
	"context"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/bitutil"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/bloombits"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
)

const (
	bloomServiceThreads = 16

	bloomFilterThreads = 3

	bloomRetrievalBatch = 16

	bloomRetrievalWait = time.Duration(0)
)

func (can *CANChain) startBloomHandlers(sectionSize uint64) {
	for i := 0; i < bloomServiceThreads; i++ {
		go func() {
			for {
				select {
				case <-can.shutdownChan:
					return

				case request := <-can.bloomRequests:
					task := <-request
					task.Bitsets = make([][]byte, len(task.Sections))
					for i, section := range task.Sections {
						head := rawdb.ReadCanonicalHash(can.chainDb, (section+1)*sectionSize-1)
						if compVector, err := rawdb.ReadBloomBits(can.chainDb, task.Bit, section, head); err == nil {
							if blob, err := bitutil.DecompressBytes(compVector, int(sectionSize/8)); err == nil {
								task.Bitsets[i] = blob
							} else {
								task.Error = err
							}
						} else {
							task.Error = err
						}
					}
					request <- task
				}
			}
		}()
	}
}

const (
	bloomThrottling = 100 * time.Millisecond
)

type BloomIndexer struct {
	size    uint64
	db      candb.Database
	gen     *bloombits.Generator
	section uint64
	head    common.Hash
}

func NewBloomIndexer(db candb.Database, size, confirms uint64) *kernel.ChainIndexer {
	backend := &BloomIndexer{
		db:   db,
		size: size,
	}
	table := candb.NewTable(db, string(rawdb.BloomBitsIndexPrefix))

	return kernel.NewChainIndexer(db, table, backend, size, confirms, bloomThrottling, "bloombits")
}

func (b *BloomIndexer) Reset(ctx context.Context, section uint64, lastSectionHead common.Hash) error {
	gen, err := bloombits.NewGenerator(uint(b.size))
	b.gen, b.section, b.head = gen, section, common.Hash{}
	return err
}

func (b *BloomIndexer) Process(ctx context.Context, header *types.Header) error {
	b.gen.AddBloom(uint(header.Number.Uint64()-b.section*b.size), header.Bloom)
	b.head = header.Hash()
	return nil
}

func (b *BloomIndexer) Commit() error {
	batch := b.db.NewBatch()
	for i := 0; i < types.BloomBitLength; i++ {
		bits, err := b.gen.Bitset(uint(i))
		if err != nil {
			return err
		}
		rawdb.WriteBloomBits(batch, uint(i), b.section, b.head, bitutil.CompressBytes(bits))
	}
	return batch.Write()
}
