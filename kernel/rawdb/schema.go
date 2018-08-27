package rawdb

import (
	"encoding/binary"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/lib/metrics"
)

var (
	databaseVerisionKey = []byte("DatabaseVersion")

	headHeaderKey = []byte("LastHeader")

	headBlockKey = []byte("LastBlock")

	headFastBlockKey = []byte("LastFast")

	fastTrieProgressKey = []byte("TrieSync")

	headerPrefix       = []byte("h")
	headerTDSuffix     = []byte("t")
	headerHashSuffix   = []byte("n")
	headerNumberPrefix = []byte("H")

	blockBodyPrefix     = []byte("b")
	blockReceiptsPrefix = []byte("r")

	txLookupPrefix  = []byte("l")
	bloomBitsPrefix = []byte("B")

	preimagePrefix = []byte("secure-key-")
	configPrefix   = []byte("ethereum-config-")

	BloomBitsIndexPrefix = []byte("iB")

	preimageCounter    = metrics.NewRegisteredCounter("db/preimage/total", nil)
	preimageHitCounter = metrics.NewRegisteredCounter("db/preimage/hits", nil)
)

type TxLookupEntry struct {
	BlockHash  common.Hash
	BlockIndex uint64
	Index      uint64
}

func encodeBlockNumber(number uint64) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, number)
	return enc
}

func headerKey(number uint64, hash common.Hash) []byte {
	return append(append(headerPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func headerTDKey(number uint64, hash common.Hash) []byte {
	return append(headerKey(number, hash), headerTDSuffix...)
}

func headerHashKey(number uint64) []byte {
	return append(append(headerPrefix, encodeBlockNumber(number)...), headerHashSuffix...)
}

func headerNumberKey(hash common.Hash) []byte {
	return append(headerNumberPrefix, hash.Bytes()...)
}

func blockBodyKey(number uint64, hash common.Hash) []byte {
	return append(append(blockBodyPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func blockReceiptsKey(number uint64, hash common.Hash) []byte {
	return append(append(blockReceiptsPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func txLookupKey(hash common.Hash) []byte {
	return append(txLookupPrefix, hash.Bytes()...)
}

func bloomBitsKey(bit uint, section uint64, hash common.Hash) []byte {
	key := append(append(bloomBitsPrefix, make([]byte, 10)...), hash.Bytes()...)

	binary.BigEndian.PutUint16(key[1:], uint16(bit))
	binary.BigEndian.PutUint64(key[3:], section)

	return key
}

func preimageKey(hash common.Hash) []byte {
	return append(preimagePrefix, hash.Bytes()...)
}

func configKey(hash common.Hash) []byte {
	return append(configPrefix, hash.Bytes()...)
}
