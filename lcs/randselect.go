package lcs

import (
	"math/rand"
)

type wrsItem interface {
	Weight() int64
}

type weightedRandomSelect struct {
	root *wrsNode
	idx  map[wrsItem]int
}

func newWeightedRandomSelect() *weightedRandomSelect {
	return &weightedRandomSelect{root: &wrsNode{maxItems: wrsBranches}, idx: make(map[wrsItem]int)}
}

func (w *weightedRandomSelect) update(item wrsItem) {
	w.setWeight(item, item.Weight())
}

func (w *weightedRandomSelect) remove(item wrsItem) {
	w.setWeight(item, 0)
}

func (w *weightedRandomSelect) setWeight(item wrsItem, weight int64) {
	idx, ok := w.idx[item]
	if ok {
		w.root.setWeight(idx, weight)
		if weight == 0 {
			delete(w.idx, item)
		}
	} else {
		if weight != 0 {
			if w.root.itemCnt == w.root.maxItems {
				newRoot := &wrsNode{sumWeight: w.root.sumWeight, itemCnt: w.root.itemCnt, level: w.root.level + 1, maxItems: w.root.maxItems * wrsBranches}
				newRoot.items[0] = w.root
				newRoot.weights[0] = w.root.sumWeight
				w.root = newRoot
			}
			w.idx[item] = w.root.insert(item, weight)
		}
	}
}

func (w *weightedRandomSelect) choose() wrsItem {
	for {
		if w.root.sumWeight == 0 {
			return nil
		}
		val := rand.Int63n(w.root.sumWeight)
		choice, lastWeight := w.root.choose(val)
		weight := choice.Weight()
		if weight != lastWeight {
			w.setWeight(choice, weight)
		}
		if weight >= lastWeight || rand.Int63n(lastWeight) < weight {
			return choice
		}
	}
}

const wrsBranches = 8

type wrsNode struct {
	items                    [wrsBranches]interface{}
	weights                  [wrsBranches]int64
	sumWeight                int64
	level, itemCnt, maxItems int
}

func (n *wrsNode) insert(item wrsItem, weight int64) int {
	branch := 0
	for n.items[branch] != nil && (n.level == 0 || n.items[branch].(*wrsNode).itemCnt == n.items[branch].(*wrsNode).maxItems) {
		branch++
		if branch == wrsBranches {
			panic(nil)
		}
	}
	n.itemCnt++
	n.sumWeight += weight
	n.weights[branch] += weight
	if n.level == 0 {
		n.items[branch] = item
		return branch
	}
	var subNode *wrsNode
	if n.items[branch] == nil {
		subNode = &wrsNode{maxItems: n.maxItems / wrsBranches, level: n.level - 1}
		n.items[branch] = subNode
	} else {
		subNode = n.items[branch].(*wrsNode)
	}
	subIdx := subNode.insert(item, weight)
	return subNode.maxItems*branch + subIdx
}

func (n *wrsNode) setWeight(idx int, weight int64) int64 {
	if n.level == 0 {
		oldWeight := n.weights[idx]
		n.weights[idx] = weight
		diff := weight - oldWeight
		n.sumWeight += diff
		if weight == 0 {
			n.items[idx] = nil
			n.itemCnt--
		}
		return diff
	}
	branchItems := n.maxItems / wrsBranches
	branch := idx / branchItems
	diff := n.items[branch].(*wrsNode).setWeight(idx-branch*branchItems, weight)
	n.weights[branch] += diff
	n.sumWeight += diff
	if weight == 0 {
		n.itemCnt--
	}
	return diff
}

func (n *wrsNode) choose(val int64) (wrsItem, int64) {
	for i, w := range n.weights {
		if val < w {
			if n.level == 0 {
				return n.items[i].(wrsItem), n.weights[i]
			}
			return n.items[i].(*wrsNode).choose(val)
		}
		val -= w
	}
	panic(nil)
}
