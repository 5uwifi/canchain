// +build js

package ethdb_test

import (
	"github.com/5uwifi/canchain/ethdb"
)

var _ ethdb.Database = &ethdb.LDBDatabase{}
