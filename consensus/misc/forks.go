//
// (at your option) any later version.
//
//

package misc

import (
	"fmt"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/params"
)

func VerifyForkHashes(config *params.ChainConfig, header *types.Header, uncle bool) error {
	// We don't care about uncles
	if uncle {
		return nil
	}
	// If the homestead reprice hash is set, validate it
	if config.EIP150Block != nil && config.EIP150Block.Cmp(header.Number) == 0 {
		if config.EIP150Hash != (common.Hash{}) && config.EIP150Hash != header.Hash() {
			return fmt.Errorf("homestead gas reprice fork: have 0x%x, want 0x%x", header.Hash(), config.EIP150Hash)
		}
	}
	// All ok, return
	return nil
}
