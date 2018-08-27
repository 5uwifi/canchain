package rawdb

import (
	"math/big"
	"testing"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
)

func TestLookupStorage(t *testing.T) {
	db := candb.NewMemDatabase()

	tx1 := types.NewTransaction(1, common.BytesToAddress([]byte{0x11}), big.NewInt(111), 1111, big.NewInt(11111), []byte{0x11, 0x11, 0x11})
	tx2 := types.NewTransaction(2, common.BytesToAddress([]byte{0x22}), big.NewInt(222), 2222, big.NewInt(22222), []byte{0x22, 0x22, 0x22})
	tx3 := types.NewTransaction(3, common.BytesToAddress([]byte{0x33}), big.NewInt(333), 3333, big.NewInt(33333), []byte{0x33, 0x33, 0x33})
	txs := []*types.Transaction{tx1, tx2, tx3}

	block := types.NewBlock(&types.Header{Number: big.NewInt(314)}, txs, nil, nil)

	for i, tx := range txs {
		if txn, _, _, _ := ReadTransaction(db, tx.Hash()); txn != nil {
			t.Fatalf("tx #%d [%x]: non existent transaction returned: %v", i, tx.Hash(), txn)
		}
	}
	WriteBlock(db, block)
	WriteTxLookupEntries(db, block)

	for i, tx := range txs {
		if txn, hash, number, index := ReadTransaction(db, tx.Hash()); txn == nil {
			t.Fatalf("tx #%d [%x]: transaction not found", i, tx.Hash())
		} else {
			if hash != block.Hash() || number != block.NumberU64() || index != uint64(i) {
				t.Fatalf("tx #%d [%x]: positional metadata mismatch: have %x/%d/%d, want %x/%v/%v", i, tx.Hash(), hash, number, index, block.Hash(), block.NumberU64(), i)
			}
			if tx.Hash() != txn.Hash() {
				t.Fatalf("tx #%d [%x]: transaction mismatch: have %v, want %v", i, tx.Hash(), txn, tx)
			}
		}
	}
	for i, tx := range txs {
		DeleteTxLookupEntry(db, tx.Hash())
		if txn, _, _, _ := ReadTransaction(db, tx.Hash()); txn != nil {
			t.Fatalf("tx #%d [%x]: deleted transaction returned: %v", i, tx.Hash(), txn)
		}
	}
}
