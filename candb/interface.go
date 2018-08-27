package candb

const IdealBatchSize = 100 * 1024

type Putter interface {
	Put(key []byte, value []byte) error
}

type Deleter interface {
	Delete(key []byte) error
}

type Database interface {
	Putter
	Deleter
	Get(key []byte) ([]byte, error)
	Has(key []byte) (bool, error)
	Close()
	NewBatch() Batch
}

type Batch interface {
	Putter
	Deleter
	ValueSize() int
	Write() error
	Reset()
}
