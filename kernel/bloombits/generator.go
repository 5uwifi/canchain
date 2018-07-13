package bloombits

import (
	"errors"

	"github.com/5uwifi/canchain/kernel/types"
)

var errSectionOutOfBounds = errors.New("section out of bounds")

type Generator struct {
	blooms   [types.BloomBitLength][]byte // Rotated blooms for per-bit matching
	sections uint                         // Number of sections to batch together
	nextBit  uint                         // Next bit to set when adding a bloom
}

func NewGenerator(sections uint) (*Generator, error) {
	if sections%8 != 0 {
		return nil, errors.New("section count not multiple of 8")
	}
	b := &Generator{sections: sections}
	for i := 0; i < types.BloomBitLength; i++ {
		b.blooms[i] = make([]byte, sections/8)
	}
	return b, nil
}

func (b *Generator) AddBloom(index uint, bloom types.Bloom) error {
	// Make sure we're not adding more bloom filters than our capacity
	if b.nextBit >= b.sections {
		return errSectionOutOfBounds
	}
	if b.nextBit != index {
		return errors.New("bloom filter with unexpected index")
	}
	// Rotate the bloom and insert into our collection
	byteIndex := b.nextBit / 8
	bitMask := byte(1) << byte(7-b.nextBit%8)

	for i := 0; i < types.BloomBitLength; i++ {
		bloomByteIndex := types.BloomByteLength - 1 - i/8
		bloomBitMask := byte(1) << byte(i%8)

		if (bloom[bloomByteIndex] & bloomBitMask) != 0 {
			b.blooms[i][byteIndex] |= bitMask
		}
	}
	b.nextBit++

	return nil
}

func (b *Generator) Bitset(idx uint) ([]byte, error) {
	if b.nextBit != b.sections {
		return nil, errors.New("bloom not fully generated yet")
	}
	if idx >= b.sections {
		return nil, errSectionOutOfBounds
	}
	return b.blooms[idx], nil
}
