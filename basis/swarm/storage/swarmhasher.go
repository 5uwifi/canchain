
package storage

import (
	"hash"
)

const (
	BMTHash     = "BMT"
	SHA3Hash    = "SHA3" // http://golang.org/pkg/hash/#Hash
	DefaultHash = BMTHash
)

type SwarmHash interface {
	hash.Hash
	ResetWithLength([]byte)
}

type HashWithLength struct {
	hash.Hash
}

func (h *HashWithLength) ResetWithLength(length []byte) {
	h.Reset()
	h.Write(length)
}
