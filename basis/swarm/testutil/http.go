
package testutil

import (
	"context"
	"io/ioutil"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/basis/swarm/api"
	"github.com/5uwifi/canchain/basis/swarm/storage"
	"github.com/5uwifi/canchain/basis/swarm/storage/mru"
)

type TestServer interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}

type fakeBackend struct {
	blocknumber int64
}

func (f *fakeBackend) HeaderByNumber(context context.Context, _ string, bigblock *big.Int) (*types.Header, error) {
	f.blocknumber++
	biggie := big.NewInt(f.blocknumber)
	return &types.Header{
		Number: biggie,
	}, nil
}

func NewTestSwarmServer(t *testing.T, serverFunc func(*api.API) TestServer) *TestSwarmServer {
	dir, err := ioutil.TempDir("", "swarm-storage-test")
	if err != nil {
		t.Fatal(err)
	}
	storeparams := storage.NewDefaultLocalStoreParams()
	storeparams.DbCapacity = 5000000
	storeparams.CacheCapacity = 5000
	storeparams.Init(dir)
	localStore, err := storage.NewLocalStore(storeparams, nil)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	fileStore := storage.NewFileStore(localStore, storage.NewFileStoreParams())

	// mutable resources test setup
	resourceDir, err := ioutil.TempDir("", "swarm-resource-test")
	if err != nil {
		t.Fatal(err)
	}
	rhparams := &mru.HandlerParams{
		QueryMaxPeriods: &mru.LookupParams{},
		HeaderGetter: &fakeBackend{
			blocknumber: 42,
		},
	}
	rh, err := mru.NewTestHandler(resourceDir, rhparams)
	if err != nil {
		t.Fatal(err)
	}

	a := api.NewAPI(fileStore, nil, rh)
	srv := httptest.NewServer(serverFunc(a))
	return &TestSwarmServer{
		Server:    srv,
		FileStore: fileStore,
		dir:       dir,
		Hasher:    storage.MakeHashFunc(storage.DefaultHash)(),
		cleanup: func() {
			srv.Close()
			rh.Close()
			os.RemoveAll(dir)
			os.RemoveAll(resourceDir)
		},
	}
}

type TestSwarmServer struct {
	*httptest.Server
	Hasher    storage.SwarmHash
	FileStore *storage.FileStore
	dir       string
	cleanup   func()
}

func (t *TestSwarmServer) Close() {
	t.cleanup()
}
