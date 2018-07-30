package types

import (
	"bytes"
	"math/big"
	"reflect"
	"testing"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/basis/rlp"
)

// from bcValidBlockTest.json, "SimpleTx"
func TestBlockEncoding(t *testing.T) {
	blockEnc := common.FromHex("f90293f90218a09a18258f696f596774739d6298e74d0b1d84d173b03d02f2dbb3bd096e4914f2a01dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d493479a106af868c4f5d05d858dec2c6197458caa6abd32a03ccb681b12a093f429f5655baab3ff8135d7825379b1d86037e3a6805e5d5ed46f457fa11481a059ff3906659916d42274e65aa4ea28fb7cf991249dc006139eb2c9acb7bac20da00a4cb82b7dd9aa9a679921dd9c9a0c0621e73d5581074907575a34a4f8443833b9010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000830f3a000183ffc001825208845b50584599d8820100846765746888676f312e31302e338664617277696ea0353649874652f0d2891777c09838e8208d5e8167b71242ca2c1999dad6316cc18835d30e7db30e265bf875f87380850430e2340083015f909ac5389ff4212863df0988bd7b289faa3879b3938c5213f2e992ba880de0b6b3a7640000802aa003988f0d52bb400ee0ef0405ce7cc0f411e2d01161d741edb99a793700c93901a05f282b379468958dcd143f2d04ea46b50486fb16b3d65dea0007fc14f29e546fc0")
	var block Block
	if err := rlp.DecodeBytes(blockEnc, &block); err != nil {
		t.Fatal("decode error: ", err)
	}

	check := func(f string, got, want interface{}) {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s mismatch: got %v, want %v", f, got, want)
		}
	}
	check("Difficulty", block.Difficulty(), big.NewInt(997888))
	check("GasLimit", block.GasLimit(), uint64(16760833))
	check("GasUsed", block.GasUsed(), uint64(21000))
	check("Coinbase", block.Coinbase(), common.HexToAddress("0x106af868c4f5d05d858dec2c6197458caa6abd32a03ccb681b12"))
	check("MixDigest", block.MixDigest(), common.HexToHash("0x353649874652f0d2891777c09838e8208d5e8167b71242ca2c1999dad6316cc1"))
	check("Root", block.Root(), common.HexToHash("0x93f429f5655baab3ff8135d7825379b1d86037e3a6805e5d5ed46f457fa11481"))
	check("Hash", block.Hash(), common.HexToHash("0x0a3e37f4ec9c51942b41115b1e9fc8718f58d3f09f8858f5078c20d467679593"))
	check("Nonce", block.Nonce(), uint64(0x35d30e7db30e265b))
	check("Time", block.Time(), big.NewInt(1531992133))
	check("Size", block.Size(), common.StorageSize(len(blockEnc)))


	tx1 := NewTransaction(0, common.HexToAddress("0xc5389ff4212863df0988bd7b289faa3879b3938c5213f2e992ba"), big.NewInt(1000000000000000000), 90000, big.NewInt(18000000000), nil)

	tx1, _ = tx1.WithSignature(HomesteadSigner{}, common.Hex2Bytes("03988f0d52bb400ee0ef0405ce7cc0f411e2d01161d741edb99a793700c939015f282b379468958dcd143f2d04ea46b50486fb16b3d65dea0007fc14f29e546f0f"))
	check("len(Transactions)", len(block.Transactions()), 1)
	check("Transactions[0].Hash", block.Transactions()[0].Hash().Hex(), tx1.Hash().Hex())

	ourBlockEnc, err := rlp.EncodeToBytes(&block)
	if err != nil {
		t.Fatal("encode error: ", err)
	}
	if !bytes.Equal(ourBlockEnc, blockEnc) {
		t.Errorf("encoded block mismatch:\ngot:  %x\nwant: %x", ourBlockEnc, blockEnc)
	}
}
