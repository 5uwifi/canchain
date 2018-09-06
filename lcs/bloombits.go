package lcs

import (
	"time"

	"github.com/5uwifi/canchain/common/bitutil"
	"github.com/5uwifi/canchain/light"
)

const (
	bloomServiceThreads = 16

	bloomFilterThreads = 3

	bloomRetrievalBatch = 16

	bloomRetrievalWait = time.Microsecond * 100
)

func (can *LightCANChain) startBloomHandlers(sectionSize uint64) {
	for i := 0; i < bloomServiceThreads; i++ {
		go func() {
			for {
				select {
				case <-can.shutdownChan:
					return

				case request := <-can.bloomRequests:
					task := <-request
					task.Bitsets = make([][]byte, len(task.Sections))
					compVectors, err := light.GetBloomBits(task.Context, can.odr, task.Bit, task.Sections)
					if err == nil {
						for i := range task.Sections {
							if blob, err := bitutil.DecompressBytes(compVectors[i], int(sectionSize/8)); err == nil {
								task.Bitsets[i] = blob
							} else {
								task.Error = err
							}
						}
					} else {
						task.Error = err
					}
					request <- task
				}
			}
		}()
	}
}
