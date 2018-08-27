package guide

import (
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/5uwifi/canchain/accounts/keystore"
	"github.com/5uwifi/canchain/kernel/types"
)

func TestAccountManagement(t *testing.T) {
	workdir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("Failed to create temporary work dir: %v", err)
	}
	defer os.RemoveAll(workdir)

	ks := keystore.NewKeyStore(filepath.Join(workdir, "keystore"), keystore.StandardScryptN, keystore.StandardScryptP)

	newAcc, err := ks.NewAccount("Creation password")
	if err != nil {
		t.Fatalf("Failed to create new account: %v", err)
	}
	jsonAcc, err := ks.Export(newAcc, "Creation password", "Export password")
	if err != nil {
		t.Fatalf("Failed to export account: %v", err)
	}
	if err := ks.Update(newAcc, "Creation password", "Update password"); err != nil {
		t.Fatalf("Failed to update account: %v", err)
	}
	if err := ks.Delete(newAcc, "Update password"); err != nil {
		t.Fatalf("Failed to delete account: %v", err)
	}
	if _, err := ks.Import(jsonAcc, "Export password", "Import password"); err != nil {
		t.Fatalf("Failed to import account: %v", err)
	}
	signer, err := ks.NewAccount("Signer password")
	if err != nil {
		t.Fatalf("Failed to create signer account: %v", err)
	}
	tx, chain := new(types.Transaction), big.NewInt(1)

	if _, err := ks.SignTxWithPassphrase(signer, "Signer password", tx, chain); err != nil {
		t.Fatalf("Failed to sign with passphrase: %v", err)
	}
	if err := ks.Unlock(signer, "Signer password"); err != nil {
		t.Fatalf("Failed to unlock account: %v", err)
	}
	if _, err := ks.SignTx(signer, tx, chain); err != nil {
		t.Fatalf("Failed to sign with unlocked account: %v", err)
	}
	if err := ks.Lock(signer.Address); err != nil {
		t.Fatalf("Failed to lock account: %v", err)
	}
	if err := ks.TimedUnlock(signer, "Signer password", time.Second); err != nil {
		t.Fatalf("Failed to time unlock account: %v", err)
	}
	if _, err := ks.SignTx(signer, tx, chain); err != nil {
		t.Fatalf("Failed to sign with time unlocked account: %v", err)
	}
}
