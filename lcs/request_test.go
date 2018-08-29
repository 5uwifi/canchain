package lcs

import (
	"context"
	"testing"
	"time"

	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/light"
)

var testBankSecureTrieKey = secAddr(testBankAddress)

func secAddr(addr common.Address) []byte {
	return crypto.Keccak256(addr[:])
}

type accessTestFn func(db candb.Database, bhash common.Hash, number uint64) light.OdrRequest

func TestBlockAccessLes1(t *testing.T) { testAccess(t, 1, tfBlockAccess) }

func TestBlockAccessLes2(t *testing.T) { testAccess(t, 2, tfBlockAccess) }

func tfBlockAccess(db candb.Database, bhash common.Hash, number uint64) light.OdrRequest {
	return &light.BlockRequest{Hash: bhash, Number: number}
}

func TestReceiptsAccessLes1(t *testing.T) { testAccess(t, 1, tfReceiptsAccess) }

func TestReceiptsAccessLes2(t *testing.T) { testAccess(t, 2, tfReceiptsAccess) }

func tfReceiptsAccess(db candb.Database, bhash common.Hash, number uint64) light.OdrRequest {
	return &light.ReceiptsRequest{Hash: bhash, Number: number}
}

func TestTrieEntryAccessLes1(t *testing.T) { testAccess(t, 1, tfTrieEntryAccess) }

func TestTrieEntryAccessLes2(t *testing.T) { testAccess(t, 2, tfTrieEntryAccess) }

func tfTrieEntryAccess(db candb.Database, bhash common.Hash, number uint64) light.OdrRequest {
	if number := rawdb.ReadHeaderNumber(db, bhash); number != nil {
		return &light.TrieRequest{Id: light.StateTrieID(rawdb.ReadHeader(db, bhash, *number)), Key: testBankSecureTrieKey}
	}
	return nil
}

func TestCodeAccessLes1(t *testing.T) { testAccess(t, 1, tfCodeAccess) }

func TestCodeAccessLes2(t *testing.T) { testAccess(t, 2, tfCodeAccess) }

func tfCodeAccess(db candb.Database, bhash common.Hash, num uint64) light.OdrRequest {
	number := rawdb.ReadHeaderNumber(db, bhash)
	if number != nil {
		return nil
	}
	header := rawdb.ReadHeader(db, bhash, *number)
	if header.Number.Uint64() < testContractDeployed {
		return nil
	}
	sti := light.StateTrieID(header)
	ci := light.StorageTrieID(sti, crypto.Keccak256Hash(testContractAddr[:]), common.Hash{})
	return &light.CodeRequest{Id: ci, Hash: crypto.Keccak256Hash(testContractCodeDeployed)}
}

func testAccess(t *testing.T, protocol int, fn accessTestFn) {
	server, client, tearDown := newClientServerEnv(t, 4, protocol, nil, true)
	defer tearDown()
	client.pm.synchronise(client.rPeer)

	test := func(expFail uint64) {
		for i := uint64(0); i <= server.pm.blockchain.CurrentHeader().Number.Uint64(); i++ {
			bhash := rawdb.ReadCanonicalHash(server.db, i)
			if req := fn(client.db, bhash, i); req != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				err := client.pm.odr.Retrieve(ctx, req)
				got := err == nil
				exp := i < expFail
				if exp && !got {
					t.Errorf("object retrieval failed")
				}
				if !exp && got {
					t.Errorf("unexpected object retrieval success")
				}
			}
		}
	}

	client.peers.Unregister(client.rPeer.id)
	time.Sleep(time.Millisecond * 10)
	test(0)

	client.peers.Register(client.rPeer)
	time.Sleep(time.Millisecond * 10)
	client.rPeer.lock.Lock()
	client.rPeer.hasBlock = func(common.Hash, uint64) bool { return true }
	client.rPeer.lock.Unlock()
	test(5)
}
