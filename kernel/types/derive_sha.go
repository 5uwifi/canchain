//
// (at your option) any later version.
//
//

package types

import (
	"bytes"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/rlp"
	"github.com/5uwifi/canchain/basis/trie"
)

type DerivableList interface {
	Len() int
	GetRlp(i int) []byte
}

func DeriveSha(list DerivableList) common.Hash {
	keybuf := new(bytes.Buffer)
	trie := new(trie.Trie)
	for i := 0; i < list.Len(); i++ {
		keybuf.Reset()
		rlp.Encode(keybuf, uint(i))
		trie.Update(keybuf.Bytes(), list.GetRlp(i))
	}
	return trie.Hash()
}
