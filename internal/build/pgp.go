//
// (at your option) any later version.
//
//


package build

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/crypto/openpgp"
)

//
func PGPSignFile(input string, output string, pgpkey string) error {
	// Parse the keyring and make sure we only have a single private key in it
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewBufferString(pgpkey))
	if err != nil {
		return err
	}
	if len(keys) != 1 {
		return fmt.Errorf("key count mismatch: have %d, want %d", len(keys), 1)
	}
	// Create the input and output streams for signing
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()

	// Generate the signature and return
	return openpgp.ArmoredDetachSign(out, keys[0], in, nil)
}

func PGPKeyID(pgpkey string) (string, error) {
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewBufferString(pgpkey))
	if err != nil {
		return "", err
	}
	if len(keys) != 1 {
		return "", fmt.Errorf("key count mismatch: have %d, want %d", len(keys), 1)
	}
	return keys[0].PrimaryKey.KeyIdString(), nil
}
