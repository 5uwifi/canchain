
package mru

import (
	"crypto/ecdsa"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/crypto"
)

type Signer interface {
	Sign(common.Hash) (Signature, error)
}

type GenericSigner struct {
	PrivKey *ecdsa.PrivateKey
}

func (self *GenericSigner) Sign(data common.Hash) (signature Signature, err error) {
	signaturebytes, err := crypto.Sign(data.Bytes(), self.PrivKey)
	if err != nil {
		return
	}
	copy(signature[:], signaturebytes)
	return
}
