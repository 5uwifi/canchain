package keystore

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/cespare/cp"
	"github.com/davecgh/go-spew/spew"
	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/common"
)

var (
	cachetestDir, _   = filepath.Abs(filepath.Join("testdata", "keystore"))
	cachetestAccounts = []accounts.Account{
		{
			Address: common.HexToAddress("7ef5a6135f1fd6a02593eedc869c6d41d934aef8"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(cachetestDir, "UTC--2016-03-22T12-57-55.920751759Z--7ef5a6135f1fd6a02593eedc869c6d41d934aef8")},
		},
		{
			Address: common.HexToAddress("f466859ead1932d743d622cb74fc058882e8648a"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(cachetestDir, "aaa")},
		},
		{
			Address: common.HexToAddress("289d485d9771714cce91d3393d764e1311907acc"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(cachetestDir, "zzz")},
		},
	}
)

func TestWatchNewFile(t *testing.T) {
	t.Parallel()

	dir, ks := tmpKeyStore(t, false)
	defer os.RemoveAll(dir)

	ks.Accounts()
	time.Sleep(1000 * time.Millisecond)

	wantAccounts := make([]accounts.Account, len(cachetestAccounts))
	for i := range cachetestAccounts {
		wantAccounts[i] = accounts.Account{
			Address: cachetestAccounts[i].Address,
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(dir, filepath.Base(cachetestAccounts[i].URL.Path))},
		}
		if err := cp.CopyFile(wantAccounts[i].URL.Path, cachetestAccounts[i].URL.Path); err != nil {
			t.Fatal(err)
		}
	}

	var list []accounts.Account
	for d := 200 * time.Millisecond; d < 5*time.Second; d *= 2 {
		list = ks.Accounts()
		if reflect.DeepEqual(list, wantAccounts) {
			select {
			case <-ks.changes:
			default:
				t.Fatalf("wasn't notified of new accounts")
			}
			return
		}
		time.Sleep(d)
	}
	t.Errorf("got %s, want %s", spew.Sdump(list), spew.Sdump(wantAccounts))
}

func TestWatchNoDir(t *testing.T) {
	t.Parallel()

	rand.Seed(time.Now().UnixNano())
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("eth-keystore-watch-test-%d-%d", os.Getpid(), rand.Int()))
	ks := NewKeyStore(dir, LightScryptN, LightScryptP)

	list := ks.Accounts()
	if len(list) > 0 {
		t.Error("initial account list not empty:", list)
	}
	time.Sleep(100 * time.Millisecond)

	os.MkdirAll(dir, 0700)
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "aaa")
	if err := cp.CopyFile(file, cachetestAccounts[0].URL.Path); err != nil {
		t.Fatal(err)
	}

	wantAccounts := []accounts.Account{cachetestAccounts[0]}
	wantAccounts[0].URL = accounts.URL{Scheme: KeyStoreScheme, Path: file}
	for d := 200 * time.Millisecond; d < 8*time.Second; d *= 2 {
		list = ks.Accounts()
		if reflect.DeepEqual(list, wantAccounts) {
			select {
			case <-ks.changes:
			default:
				t.Fatalf("wasn't notified of new accounts")
			}
			return
		}
		time.Sleep(d)
	}
	t.Errorf("\ngot  %v\nwant %v", list, wantAccounts)
}

func TestCacheInitialReload(t *testing.T) {
	cache, _ := newAccountCache(cachetestDir)
	accounts := cache.accounts()
	if !reflect.DeepEqual(accounts, cachetestAccounts) {
		t.Fatalf("got initial accounts: %swant %s", spew.Sdump(accounts), spew.Sdump(cachetestAccounts))
	}
}

func TestCacheAddDeleteOrder(t *testing.T) {
	cache, _ := newAccountCache("testdata/no-such-dir")
	cache.watcher.running = true

	accs := []accounts.Account{
		{
			Address: common.HexToAddress("095e7baea6a6c7c4c2dfeb977efac326af552d87"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: "-309830980"},
		},
		{
			Address: common.HexToAddress("2cac1adea150210703ba75ed097ddfe24e14f213"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: "ggg"},
		},
		{
			Address: common.HexToAddress("8bda78331c916a08481428e4b07c96d3e916d165"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: "zzzzzz-the-very-last-one.keyXXX"},
		},
		{
			Address: common.HexToAddress("d49ff4eeb0b2686ed89c0fc0f2b6ea533ddbbd5e"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: "SOMETHING.key"},
		},
		{
			Address: common.HexToAddress("7ef5a6135f1fd6a02593eedc869c6d41d934aef8"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: "UTC--2016-03-22T12-57-55.920751759Z--7ef5a6135f1fd6a02593eedc869c6d41d934aef8"},
		},
		{
			Address: common.HexToAddress("f466859ead1932d743d622cb74fc058882e8648a"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: "aaa"},
		},
		{
			Address: common.HexToAddress("289d485d9771714cce91d3393d764e1311907acc"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: "zzz"},
		},
	}
	for _, a := range accs {
		cache.add(a)
	}
	cache.add(accs[0])
	cache.add(accs[2])

	wantAccounts := make([]accounts.Account, len(accs))
	copy(wantAccounts, accs)
	sort.Sort(accountsByURL(wantAccounts))
	list := cache.accounts()
	if !reflect.DeepEqual(list, wantAccounts) {
		t.Fatalf("got accounts: %s\nwant %s", spew.Sdump(accs), spew.Sdump(wantAccounts))
	}
	for _, a := range accs {
		if !cache.hasAddress(a.Address) {
			t.Errorf("expected hasAccount(%x) to return true", a.Address)
		}
	}
	if cache.hasAddress(common.HexToAddress("fd9bd350f08ee3c0c19b85a8e16114a11a60aa4e")) {
		t.Errorf("expected hasAccount(%x) to return false", common.HexToAddress("fd9bd350f08ee3c0c19b85a8e16114a11a60aa4e"))
	}

	for i := 0; i < len(accs); i += 2 {
		cache.delete(wantAccounts[i])
	}
	cache.delete(accounts.Account{Address: common.HexToAddress("fd9bd350f08ee3c0c19b85a8e16114a11a60aa4e"), URL: accounts.URL{Scheme: KeyStoreScheme, Path: "something"}})

	wantAccountsAfterDelete := []accounts.Account{
		wantAccounts[1],
		wantAccounts[3],
		wantAccounts[5],
	}
	list = cache.accounts()
	if !reflect.DeepEqual(list, wantAccountsAfterDelete) {
		t.Fatalf("got accounts after delete: %s\nwant %s", spew.Sdump(list), spew.Sdump(wantAccountsAfterDelete))
	}
	for _, a := range wantAccountsAfterDelete {
		if !cache.hasAddress(a.Address) {
			t.Errorf("expected hasAccount(%x) to return true", a.Address)
		}
	}
	if cache.hasAddress(wantAccounts[0].Address) {
		t.Errorf("expected hasAccount(%x) to return false", wantAccounts[0].Address)
	}
}

func TestCacheFind(t *testing.T) {
	dir := filepath.Join("testdata", "dir")
	cache, _ := newAccountCache(dir)
	cache.watcher.running = true

	accs := []accounts.Account{
		{
			Address: common.HexToAddress("095e7baea6a6c7c4c2dfeb977efac326af552d87"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(dir, "a.key")},
		},
		{
			Address: common.HexToAddress("2cac1adea150210703ba75ed097ddfe24e14f213"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(dir, "b.key")},
		},
		{
			Address: common.HexToAddress("d49ff4eeb0b2686ed89c0fc0f2b6ea533ddbbd5e"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(dir, "c.key")},
		},
		{
			Address: common.HexToAddress("d49ff4eeb0b2686ed89c0fc0f2b6ea533ddbbd5e"),
			URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(dir, "c2.key")},
		},
	}
	for _, a := range accs {
		cache.add(a)
	}

	nomatchAccount := accounts.Account{
		Address: common.HexToAddress("f466859ead1932d743d622cb74fc058882e8648a"),
		URL:     accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Join(dir, "something")},
	}
	tests := []struct {
		Query      accounts.Account
		WantResult accounts.Account
		WantError  error
	}{
		{Query: accounts.Account{Address: accs[0].Address}, WantResult: accs[0]},
		{Query: accounts.Account{URL: accs[0].URL}, WantResult: accs[0]},
		{Query: accounts.Account{URL: accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Base(accs[0].URL.Path)}}, WantResult: accs[0]},
		{Query: accs[0], WantResult: accs[0]},
		{Query: accs[2], WantResult: accs[2]},
		{
			Query: accounts.Account{Address: accs[2].Address},
			WantError: &AmbiguousAddrError{
				Addr:    accs[2].Address,
				Matches: []accounts.Account{accs[2], accs[3]},
			},
		},
		{Query: nomatchAccount, WantError: ErrNoMatch},
		{Query: accounts.Account{URL: nomatchAccount.URL}, WantError: ErrNoMatch},
		{Query: accounts.Account{URL: accounts.URL{Scheme: KeyStoreScheme, Path: filepath.Base(nomatchAccount.URL.Path)}}, WantError: ErrNoMatch},
		{Query: accounts.Account{Address: nomatchAccount.Address}, WantError: ErrNoMatch},
	}
	for i, test := range tests {
		a, err := cache.find(test.Query)
		if !reflect.DeepEqual(err, test.WantError) {
			t.Errorf("test %d: error mismatch for query %v\ngot %q\nwant %q", i, test.Query, err, test.WantError)
			continue
		}
		if a != test.WantResult {
			t.Errorf("test %d: result mismatch for query %v\ngot %v\nwant %v", i, test.Query, a, test.WantResult)
			continue
		}
	}
}

func waitForAccounts(wantAccounts []accounts.Account, ks *KeyStore) error {
	var list []accounts.Account
	for d := 200 * time.Millisecond; d < 8*time.Second; d *= 2 {
		list = ks.Accounts()
		if reflect.DeepEqual(list, wantAccounts) {
			select {
			case <-ks.changes:
			default:
				return fmt.Errorf("wasn't notified of new accounts")
			}
			return nil
		}
		time.Sleep(d)
	}
	return fmt.Errorf("\ngot  %v\nwant %v", list, wantAccounts)
}

func TestUpdatedKeyfileContents(t *testing.T) {
	t.Parallel()

	rand.Seed(time.Now().UnixNano())
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("eth-keystore-watch-test-%d-%d", os.Getpid(), rand.Int()))
	ks := NewKeyStore(dir, LightScryptN, LightScryptP)

	list := ks.Accounts()
	if len(list) > 0 {
		t.Error("initial account list not empty:", list)
	}
	time.Sleep(100 * time.Millisecond)

	os.MkdirAll(dir, 0700)
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "aaa")

	if err := cp.CopyFile(file, cachetestAccounts[0].URL.Path); err != nil {
		t.Fatal(err)
	}

	wantAccounts := []accounts.Account{cachetestAccounts[0]}
	wantAccounts[0].URL = accounts.URL{Scheme: KeyStoreScheme, Path: file}
	if err := waitForAccounts(wantAccounts, ks); err != nil {
		t.Error(err)
		return
	}

	time.Sleep(1000 * time.Millisecond)

	if err := forceCopyFile(file, cachetestAccounts[1].URL.Path); err != nil {
		t.Fatal(err)
		return
	}
	wantAccounts = []accounts.Account{cachetestAccounts[1]}
	wantAccounts[0].URL = accounts.URL{Scheme: KeyStoreScheme, Path: file}
	if err := waitForAccounts(wantAccounts, ks); err != nil {
		t.Errorf("First replacement failed")
		t.Error(err)
		return
	}

	time.Sleep(1000 * time.Millisecond)

	if err := forceCopyFile(file, cachetestAccounts[2].URL.Path); err != nil {
		t.Fatal(err)
		return
	}
	wantAccounts = []accounts.Account{cachetestAccounts[2]}
	wantAccounts[0].URL = accounts.URL{Scheme: KeyStoreScheme, Path: file}
	if err := waitForAccounts(wantAccounts, ks); err != nil {
		t.Errorf("Second replacement failed")
		t.Error(err)
		return
	}

	time.Sleep(1000 * time.Millisecond)

	if err := ioutil.WriteFile(file, []byte("foo"), 0644); err != nil {
		t.Fatal(err)
		return
	}
	if err := waitForAccounts([]accounts.Account{}, ks); err != nil {
		t.Errorf("Emptying account file failed")
		t.Error(err)
		return
	}
}

func forceCopyFile(dst, src string) error {
	data, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(dst, data, 0644)
}
