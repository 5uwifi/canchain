//
// (at your option) any later version.
//
//


package whisperv5

import (
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
)

type TopicType [TopicLength]byte

func BytesToTopic(b []byte) (t TopicType) {
	sz := TopicLength
	if x := len(b); x < TopicLength {
		sz = x
	}
	for i := 0; i < sz; i++ {
		t[i] = b[i]
	}
	return t
}

func (t *TopicType) String() string {
	return common.ToHex(t[:])
}

func (t TopicType) MarshalText() ([]byte, error) {
	return hexutil.Bytes(t[:]).MarshalText()
}

func (t *TopicType) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("Topic", input, t[:])
}
