package p2p

import (
	"fmt"

	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/enr"
)

type Protocol struct {
	Name string

	Version uint

	Length uint64

	Run func(peer *Peer, rw MsgReadWriter) error

	NodeInfo func() interface{}

	PeerInfo func(id cnode.ID) interface{}

	Attributes []enr.Entry
}

func (p Protocol) cap() Cap {
	return Cap{p.Name, p.Version}
}

type Cap struct {
	Name    string
	Version uint
}

func (cap Cap) String() string {
	return fmt.Sprintf("%s/%d", cap.Name, cap.Version)
}

type capsByNameAndVersion []Cap

func (cs capsByNameAndVersion) Len() int      { return len(cs) }
func (cs capsByNameAndVersion) Swap(i, j int) { cs[i], cs[j] = cs[j], cs[i] }
func (cs capsByNameAndVersion) Less(i, j int) bool {
	return cs[i].Name < cs[j].Name || (cs[i].Name == cs[j].Name && cs[i].Version < cs[j].Version)
}

func (capsByNameAndVersion) ENRKey() string { return "cap" }
