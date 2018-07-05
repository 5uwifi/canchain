//
// (at your option) any later version.
//
//

package api

import (
	"github.com/5uwifi/canchain/basis/swarm/network"
)

type Control struct {
	api  *API
	hive *network.Hive
}

func NewControl(api *API, hive *network.Hive) *Control {
	return &Control{api, hive}
}

//	self.hive.BlockNetworkRead(on)
//}
//
//	self.hive.SyncEnabled(on)
//}
//
//	self.hive.SwapEnabled(on)
//}
//
func (c *Control) Hive() string {
	return c.hive.String()
}
