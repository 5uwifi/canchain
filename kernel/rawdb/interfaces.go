//
// (at your option) any later version.
//
//

package rawdb

type DatabaseReader interface {
	Has(key []byte) (bool, error)
	Get(key []byte) ([]byte, error)
}

type DatabaseWriter interface {
	Put(key []byte, value []byte) error
}

type DatabaseDeleter interface {
	Delete(key []byte) error
}
