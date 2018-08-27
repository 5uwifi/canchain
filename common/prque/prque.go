
package prque

import (
	"container/heap"
)

type Prque struct {
	cont *sstack
}

func New(setIndex setIndexCallback) *Prque {
	return &Prque{newSstack(setIndex)}
}

func (p *Prque) Push(data interface{}, priority int64) {
	heap.Push(p.cont, &item{data, priority})
}

func (p *Prque) Pop() (interface{}, int64) {
	item := heap.Pop(p.cont).(*item)
	return item.value, item.priority
}

func (p *Prque) PopItem() interface{} {
	return heap.Pop(p.cont).(*item).value
}

func (p *Prque) Remove(i int) interface{} {
	if i < 0 {
		return nil
	}
	return heap.Remove(p.cont, i)
}

func (p *Prque) Empty() bool {
	return p.cont.Len() == 0
}

func (p *Prque) Size() int {
	return p.cont.Len()
}

func (p *Prque) Reset() {
	*p = *New(p.cont.setIndex)
}
