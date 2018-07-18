package fuse

import (
	"sync"
	"time"

	"github.com/5uwifi/canchain/basis/swarm/api"
)

const (
	Swarmfs_Version = "0.1"
	mountTimeout    = time.Second * 5
	unmountTimeout  = time.Second * 10
	maxFuseMounts   = 5
)

var (
	swarmfs     *SwarmFS // Swarm file system singleton
	swarmfsLock sync.Once

	inode     uint64 = 1 // global inode
	inodeLock sync.RWMutex
)

type SwarmFS struct {
	swarmApi     *api.API
	activeMounts map[string]*MountInfo
	swarmFsLock  *sync.RWMutex
}

func NewSwarmFS(api *api.API) *SwarmFS {
	swarmfsLock.Do(func() {
		swarmfs = &SwarmFS{
			swarmApi:     api,
			swarmFsLock:  &sync.RWMutex{},
			activeMounts: map[string]*MountInfo{},
		}
	})
	return swarmfs

}

func NewInode() uint64 {
	inodeLock.Lock()
	defer inodeLock.Unlock()
	inode += 1
	return inode
}
