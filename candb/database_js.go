// +build js

package ethdb

import (
	"errors"
)

var errNotSupported = errors.New("ethdb: not supported")

type LDBDatabase struct {
}

func NewLDBDatabase(file string, cache int, handles int) (*LDBDatabase, error) {
	return nil, errNotSupported
}

func (db *LDBDatabase) Path() string {
	return ""
}

func (db *LDBDatabase) Put(key []byte, value []byte) error {
	return errNotSupported
}

func (db *LDBDatabase) Has(key []byte) (bool, error) {
	return false, errNotSupported
}

func (db *LDBDatabase) Get(key []byte) ([]byte, error) {
	return nil, errNotSupported
}

func (db *LDBDatabase) Delete(key []byte) error {
	return errNotSupported
}

func (db *LDBDatabase) Close() {
}

func (db *LDBDatabase) Meter(prefix string) {
}

func (db *LDBDatabase) NewBatch() Batch {
	return nil
}
