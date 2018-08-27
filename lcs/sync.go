package lcs

import (
	"context"
	"time"

	"github.com/5uwifi/canchain/can/downloader"
	"github.com/5uwifi/canchain/kernel/rawdb"
	"github.com/5uwifi/canchain/light"
)

func (pm *ProtocolManager) syncer() {
	defer pm.downloader.Terminate()

	for {
		select {
		case <-pm.newPeerCh:
			/*
				if pm.peers.Len() < minDesiredPeerCount {
					break
				}
				go pm.synchronise(pm.peers.BestPeer())
			*/
		/*case <-forceSync:
		go pm.synchronise(pm.peers.BestPeer())
		*/
		case <-pm.noMorePeers:
			return
		}
	}
}

func (pm *ProtocolManager) needToSync(peerHead blockInfo) bool {
	head := pm.blockchain.CurrentHeader()
	currentTd := rawdb.ReadTd(pm.chainDb, head.Hash(), head.Number.Uint64())
	return currentTd != nil && peerHead.Td.Cmp(currentTd) > 0
}

func (pm *ProtocolManager) synchronise(peer *peer) {
	if peer == nil {
		return
	}

	if !pm.needToSync(peer.headBlockInfo()) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	pm.blockchain.(*light.LightChain).SyncCht(ctx)
	pm.downloader.Synchronise(peer.id, peer.Head(), peer.Td(), downloader.LightSync)
}
