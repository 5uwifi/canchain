
//
// * TestRefHasher
// * testBMTHasherCorrectness function
package bmt

import (
	"hash"
)

type RefHasher struct {
	maxDataLength int       // c * hashSize, where c = 2 ^ ceil(log2(count)), where count = ceil(length / hashSize)
	sectionLength int       // 2 * hashSize
	hasher        hash.Hash // base hash func (Keccak256 SHA3)
}

func NewRefHasher(hasher BaseHasherFunc, count int) *RefHasher {
	h := hasher()
	hashsize := h.Size()
	c := 2
	for ; c < count; c *= 2 {
	}
	return &RefHasher{
		sectionLength: 2 * hashsize,
		maxDataLength: c * hashsize,
		hasher:        h,
	}
}

func (rh *RefHasher) Hash(data []byte) []byte {
	// if data is shorter than the base length (maxDataLength), we provide padding with zeros
	d := make([]byte, rh.maxDataLength)
	length := len(data)
	if length > rh.maxDataLength {
		length = rh.maxDataLength
	}
	copy(d, data[:length])
	return rh.hash(d, rh.maxDataLength)
}

func (rh *RefHasher) hash(data []byte, length int) []byte {
	var section []byte
	if length == rh.sectionLength {
		// section contains two data segments (d)
		section = data
	} else {
		// section contains hashes of left and right BMT subtreea
		// to be calculated by calling hash recursively on left and right half of d
		length /= 2
		section = append(rh.hash(data[:length], length), rh.hash(data[length:], length)...)
	}
	rh.hasher.Reset()
	rh.hasher.Write(section)
	s := rh.hasher.Sum(nil)
	return s
}
