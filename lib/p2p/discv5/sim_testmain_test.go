// +build go1.4,nacl,faketime_simulation

package discv5

import (
	"os"
	"runtime"
	"testing"
	"unsafe"
)


//go:linkname faketime runtime.faketime
var faketime = 1

func TestMain(m *testing.M) {
	// We need to use unsafe somehow in order to get access to go:linkname.
	_ = unsafe.Sizeof(0)

	runtime.GOMAXPROCS(8)
	os.Exit(m.Run())
}
