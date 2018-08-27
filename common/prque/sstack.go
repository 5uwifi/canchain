
package prque

const blockSize = 4096

type item struct {
	value    interface{}
	priority int64
}

type setIndexCallback func(a interface{}, i int)

type sstack struct {
	setIndex setIndexCallback
	size     int
	capacity int
	offset   int

	blocks [][]*item
	active []*item
}

func newSstack(setIndex setIndexCallback) *sstack {
	result := new(sstack)
	result.setIndex = setIndex
	result.active = make([]*item, blockSize)
	result.blocks = [][]*item{result.active}
	result.capacity = blockSize
	return result
}

func (s *sstack) Push(data interface{}) {
	if s.size == s.capacity {
		s.active = make([]*item, blockSize)
		s.blocks = append(s.blocks, s.active)
		s.capacity += blockSize
		s.offset = 0
	} else if s.offset == blockSize {
		s.active = s.blocks[s.size/blockSize]
		s.offset = 0
	}
	if s.setIndex != nil {
		s.setIndex(data.(*item).value, s.size)
	}
	s.active[s.offset] = data.(*item)
	s.offset++
	s.size++
}

func (s *sstack) Pop() (res interface{}) {
	s.size--
	s.offset--
	if s.offset < 0 {
		s.offset = blockSize - 1
		s.active = s.blocks[s.size/blockSize]
	}
	res, s.active[s.offset] = s.active[s.offset], nil
	if s.setIndex != nil {
		s.setIndex(res.(*item).value, -1)
	}
	return
}

func (s *sstack) Len() int {
	return s.size
}

func (s *sstack) Less(i, j int) bool {
	return (s.blocks[i/blockSize][i%blockSize].priority - s.blocks[j/blockSize][j%blockSize].priority) > 0
}

func (s *sstack) Swap(i, j int) {
	ib, io, jb, jo := i/blockSize, i%blockSize, j/blockSize, j%blockSize
	a, b := s.blocks[jb][jo], s.blocks[ib][io]
	if s.setIndex != nil {
		s.setIndex(a.value, i)
		s.setIndex(b.value, j)
	}
	s.blocks[ib][io], s.blocks[jb][jo] = a, b
}

func (s *sstack) Reset() {
	*s = *newSstack(s.setIndex)
}
