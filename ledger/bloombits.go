//
// (at your option) any later version.
//
//

package ledger

import (
	"time"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/bitutil"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/bloombits"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/params"
)

const (
	// bloomServiceThreads is the number of goroutines used globally by an Ethereum
	// instance to service bloombits lookups for all running filters.
	bloomServiceThreads = 16

	// bloomFilterThreads is the number of goroutines used locally per filter to
	// multiplex requests onto the global servicing goroutines.
	bloomFilterThreads = 3

	// bloomRetrievalBatch is the maximum number of bloom bit retrievals to service
	// in a single batch.
	bloomRetrievalBatch = 16

	// bloomRetrievalWait is the maximum time to wait for enough bloom bit requests
	// to accumulate request an entire batch (avoiding hysteresis).
	bloomRetrievalWait = time.Duration(0)
)

func (eth *Ledger) startBloomHandlers() {
	for i := 0; i < bloomServiceThreads; i++ {
		go func() {
			for {
				select {
				case <-eth.shutdownChan:
					return

				case request := <-eth.bloomRequests:
					task := <-request
					task.Bitsets = make([][]byte, len(task.Sections))
					for i, section := range task.Sections {
						head := rawdb.ReadCanonicalHash(eth.chainDb, (section+1)*params.BloomBitsBlocks-1)
						if compVector, err := rawdb.ReadBloomBits(eth.chainDb, task.Bit, section, head); err == nil {
							if blob, err := bitutil.DecompressBytes(compVector, int(params.BloomBitsBlocks)/8); err == nil {
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
	// bloomConfirms is the number of confirmation blocks before a bloom section is
	// considered probably final and its rotated bits are calculated.
	bloomConfirms = 256

	// bloomThrottling is the time to wait between processing two consecutive index
	// sections. It's useful during chain upgrades to prevent disk overload.
	bloomThrottling = 100 * time.Millisecond
)

type BloomIndexer struct {
	size uint64 // section size to generate bloombits for

	db  candb.Database       // database instance to write index data and metadata into
	gen *bloombits.Generator // generator to rotate the bloom bits crating the bloom index

	section uint64      // Section is the section number being processed currently
	head    common.Hash // Head is the hash of the last header processed
}

func NewBloomIndexer(db candb.Database, size uint64) *kernel.ChainIndexer {
	backend := &BloomIndexer{
		db:   db,
		size: size,
	}
	table := candb.NewTable(db, string(rawdb.BloomBitsIndexPrefix))

	return kernel.NewChainIndexer(db, table, backend, size, bloomConfirms, bloomThrottling, "bloombits")
}

func (b *BloomIndexer) Reset(section uint64, lastSectionHead common.Hash) error {
	gen, err := bloombits.NewGenerator(uint(b.size))
	b.gen, b.section, b.head = gen, section, common.Hash{}
	return err
}

func (b *BloomIndexer) Process(header *types.Header) {
	b.gen.AddBloom(uint(header.Number.Uint64()-b.section*b.size), header.Bloom)
	b.head = header.Hash()
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
