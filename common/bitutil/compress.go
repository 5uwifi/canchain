package bitutil

import "errors"

var (
	// errMissingData is returned from decompression if the byte referenced by
	// the bitset header overflows the input data.
	errMissingData = errors.New("missing bytes on input")

	// errUnreferencedData is returned from decompression if not all bytes were used
	// up from the input data after decompressing it.
	errUnreferencedData = errors.New("extra bytes on input")

	// errExceededTarget is returned from decompression if the bitset header has
	// more bits defined than the number of target buffer space available.
	errExceededTarget = errors.New("target data size exceeded")

	// errZeroContent is returned from decompression if a data byte referenced in
	// the bitset header is actually a zero byte.
	errZeroContent = errors.New("zero byte in input content")
)

//
//

func CompressBytes(data []byte) []byte {
	if out := bitsetEncodeBytes(data); len(out) < len(data) {
		return out
	}
	cpy := make([]byte, len(data))
	copy(cpy, data)
	return cpy
}

func bitsetEncodeBytes(data []byte) []byte {
	// Empty slices get compressed to nil
	if len(data) == 0 {
		return nil
	}
	// One byte slices compress to nil or retain the single byte
	if len(data) == 1 {
		if data[0] == 0 {
			return nil
		}
		return data
	}
	// Calculate the bitset of set bytes, and gather the non-zero bytes
	nonZeroBitset := make([]byte, (len(data)+7)/8)
	nonZeroBytes := make([]byte, 0, len(data))

	for i, b := range data {
		if b != 0 {
			nonZeroBytes = append(nonZeroBytes, b)
			nonZeroBitset[i/8] |= 1 << byte(7-i%8)
		}
	}
	if len(nonZeroBytes) == 0 {
		return nil
	}
	return append(bitsetEncodeBytes(nonZeroBitset), nonZeroBytes...)
}

func DecompressBytes(data []byte, target int) ([]byte, error) {
	if len(data) > target {
		return nil, errExceededTarget
	}
	if len(data) == target {
		cpy := make([]byte, len(data))
		copy(cpy, data)
		return cpy, nil
	}
	return bitsetDecodeBytes(data, target)
}

func bitsetDecodeBytes(data []byte, target int) ([]byte, error) {
	out, size, err := bitsetDecodePartialBytes(data, target)
	if err != nil {
		return nil, err
	}
	if size != len(data) {
		return nil, errUnreferencedData
	}
	return out, nil
}

func bitsetDecodePartialBytes(data []byte, target int) ([]byte, int, error) {
	// Sanity check 0 targets to avoid infinite recursion
	if target == 0 {
		return nil, 0, nil
	}
	// Handle the zero and single byte corner cases
	decomp := make([]byte, target)
	if len(data) == 0 {
		return decomp, 0, nil
	}
	if target == 1 {
		decomp[0] = data[0] // copy to avoid referencing the input slice
		if data[0] != 0 {
			return decomp, 1, nil
		}
		return decomp, 0, nil
	}
	// Decompress the bitset of set bytes and distribute the non zero bytes
	nonZeroBitset, ptr, err := bitsetDecodePartialBytes(data, (target+7)/8)
	if err != nil {
		return nil, ptr, err
	}
	for i := 0; i < 8*len(nonZeroBitset); i++ {
		if nonZeroBitset[i/8]&(1<<byte(7-i%8)) != 0 {
			// Make sure we have enough data to push into the correct slot
			if ptr >= len(data) {
				return nil, 0, errMissingData
			}
			if i >= len(decomp) {
				return nil, 0, errExceededTarget
			}
			// Make sure the data is valid and push into the slot
			if data[ptr] == 0 {
				return nil, 0, errZeroContent
			}
			decomp[i] = data[ptr]
			ptr++
		}
	}
	return decomp, ptr, nil
}
