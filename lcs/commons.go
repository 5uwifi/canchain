package lcs

import (
	"fmt"
	"math/big"

	"github.com/5uwifi/canchain/can"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/light"
	"github.com/5uwifi/canchain/params"
)

type lcsCommons struct {
	config                       *can.Config
	iConfig                      *light.IndexerConfig
	chainDb                      candb.Database
	protocolManager              *ProtocolManager
	chtIndexer, bloomTrieIndexer *kernel.ChainIndexer
}

type NodeInfo struct {
	Network    uint64                   `json:"network"`
	Difficulty *big.Int                 `json:"difficulty"`
	Genesis    common.Hash              `json:"genesis"`
	Config     *params.ChainConfig      `json:"config"`
	Head       common.Hash              `json:"head"`
	CHT        params.TrustedCheckpoint `json:"cht"`
}

func (c *lcsCommons) makeProtocols(versions []uint) []p2p.Protocol {
	protos := make([]p2p.Protocol, len(versions))
	for i, version := range versions {
		version := version
		protos[i] = p2p.Protocol{
			Name:     "les",
			Version:  version,
			Length:   ProtocolLengths[version],
			NodeInfo: c.nodeInfo,
			Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				return c.protocolManager.runPeer(version, p, rw)
			},
			PeerInfo: func(id discover.NodeID) interface{} {
				if p := c.protocolManager.peers.Peer(fmt.Sprintf("%x", id[:8])); p != nil {
					return p.Info()
				}
				return nil
			},
		}
	}
	return protos
}

func (c *lcsCommons) nodeInfo() interface{} {
	var cht params.TrustedCheckpoint
	sections, _, _ := c.chtIndexer.Sections()
	sections2, _, _ := c.bloomTrieIndexer.Sections()

	if !c.protocolManager.lightSync {
		sections /= c.iConfig.PairChtSize / c.iConfig.ChtSize
	}

	if sections2 < sections {
		sections = sections2
	}
	if sections > 0 {
		sectionIndex := sections - 1
		sectionHead := c.bloomTrieIndexer.SectionHead(sectionIndex)
		var chtRoot common.Hash
		if c.protocolManager.lightSync {
			chtRoot = light.GetChtRoot(c.chainDb, sectionIndex, sectionHead)
		} else {
			idxV2 := (sectionIndex+1)*c.iConfig.PairChtSize/c.iConfig.ChtSize - 1
			chtRoot = light.GetChtRoot(c.chainDb, idxV2, sectionHead)
		}
		cht = params.TrustedCheckpoint{
			SectionIndex: sectionIndex,
			SectionHead:  sectionHead,
			CHTRoot:      chtRoot,
			BloomRoot:    light.GetBloomTrieRoot(c.chainDb, sectionIndex, sectionHead),
		}
	}

	chain := c.protocolManager.blockchain
	head := chain.CurrentHeader()
	hash := head.Hash()
	return &NodeInfo{
		Network:    c.config.NetworkId,
		Difficulty: chain.GetTd(hash, head.Number.Uint64()),
		Genesis:    chain.Genesis().Hash(),
		Config:     chain.Config(),
		Head:       chain.CurrentHeader().Hash(),
		CHT:        cht,
	}
}
