//
// (at your option) any later version.
//
//

package downloader

import (
	"math/big"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/candb"
)

type FakePeer struct {
	id string
	db candb.Database
	hc *kernel.HeaderChain
	dl *Downloader
}

func NewFakePeer(id string, db candb.Database, hc *kernel.HeaderChain, dl *Downloader) *FakePeer {
	return &FakePeer{id: id, db: db, hc: hc, dl: dl}
}

func (p *FakePeer) Head() (common.Hash, *big.Int) {
	header := p.hc.CurrentHeader()
	return header.Hash(), header.Number
}

func (p *FakePeer) RequestHeadersByHash(hash common.Hash, amount int, skip int, reverse bool) error {
	var (
		headers []*types.Header
		unknown bool
	)
	for !unknown && len(headers) < amount {
		origin := p.hc.GetHeaderByHash(hash)
		if origin == nil {
			break
		}
		number := origin.Number.Uint64()
		headers = append(headers, origin)
		if reverse {
			for i := 0; i <= skip; i++ {
				if header := p.hc.GetHeader(hash, number); header != nil {
					hash = header.ParentHash
					number--
				} else {
					unknown = true
					break
				}
			}
		} else {
			var (
				current = origin.Number.Uint64()
				next    = current + uint64(skip) + 1
			)
			if header := p.hc.GetHeaderByNumber(next); header != nil {
				if p.hc.GetBlockHashesFromHash(header.Hash(), uint64(skip+1))[skip] == hash {
					hash = header.Hash()
				} else {
					unknown = true
				}
			} else {
				unknown = true
			}
		}
	}
	p.dl.DeliverHeaders(p.id, headers)
	return nil
}

func (p *FakePeer) RequestHeadersByNumber(number uint64, amount int, skip int, reverse bool) error {
	var (
		headers []*types.Header
		unknown bool
	)
	for !unknown && len(headers) < amount {
		origin := p.hc.GetHeaderByNumber(number)
		if origin == nil {
			break
		}
		if reverse {
			if number >= uint64(skip+1) {
				number -= uint64(skip + 1)
			} else {
				unknown = true
			}
		} else {
			number += uint64(skip + 1)
		}
		headers = append(headers, origin)
	}
	p.dl.DeliverHeaders(p.id, headers)
	return nil
}

func (p *FakePeer) RequestBodies(hashes []common.Hash) error {
	var (
		txs    [][]*types.Transaction
		uncles [][]*types.Header
	)
	for _, hash := range hashes {
		block := rawdb.ReadBlock(p.db, hash, *p.hc.GetBlockNumber(hash))

		txs = append(txs, block.Transactions())
		uncles = append(uncles, block.Uncles())
	}
	p.dl.DeliverBodies(p.id, txs, uncles)
	return nil
}

func (p *FakePeer) RequestReceipts(hashes []common.Hash) error {
	var receipts [][]*types.Receipt
	for _, hash := range hashes {
		receipts = append(receipts, rawdb.ReadReceipts(p.db, hash, *p.hc.GetBlockNumber(hash)))
	}
	p.dl.DeliverReceipts(p.id, receipts)
	return nil
}

func (p *FakePeer) RequestNodeData(hashes []common.Hash) error {
	var data [][]byte
	for _, hash := range hashes {
		if entry, err := p.db.Get(hash.Bytes()); err == nil {
			data = append(data, entry)
		}
	}
	p.dl.DeliverNodeData(p.id, data)
	return nil
}
