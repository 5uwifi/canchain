//
// <https://github.com/rcrowley/go-metrics>
//
package metrics

import (
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/5uwifi/canchain/basis/log4j"
)

//
var Enabled bool = false

const MetricsEnabledFlag = "metrics"
const DashboardEnabledFlag = "dashboard"

func init() {
	for _, arg := range os.Args {
		if flag := strings.TrimLeft(arg, "-"); flag == MetricsEnabledFlag || flag == DashboardEnabledFlag {
			log4j.Info("Enabling metrics collection")
			Enabled = true
		}
	}
}

func CollectProcessMetrics(refresh time.Duration) {
	// Short circuit if the metrics system is disabled
	if !Enabled {
		return
	}
	// Create the various data collectors
	memstats := make([]*runtime.MemStats, 2)
	diskstats := make([]*DiskStats, 2)
	for i := 0; i < len(memstats); i++ {
		memstats[i] = new(runtime.MemStats)
		diskstats[i] = new(DiskStats)
	}
	// Define the various metrics to collect
	memAllocs := GetOrRegisterMeter("system/memory/allocs", DefaultRegistry)
	memFrees := GetOrRegisterMeter("system/memory/frees", DefaultRegistry)
	memInuse := GetOrRegisterMeter("system/memory/inuse", DefaultRegistry)
	memPauses := GetOrRegisterMeter("system/memory/pauses", DefaultRegistry)

	var diskReads, diskReadBytes, diskWrites, diskWriteBytes Meter
	if err := ReadDiskStats(diskstats[0]); err == nil {
		diskReads = GetOrRegisterMeter("system/disk/readcount", DefaultRegistry)
		diskReadBytes = GetOrRegisterMeter("system/disk/readdata", DefaultRegistry)
		diskWrites = GetOrRegisterMeter("system/disk/writecount", DefaultRegistry)
		diskWriteBytes = GetOrRegisterMeter("system/disk/writedata", DefaultRegistry)
	} else {
		log4j.Debug("Failed to read disk metrics", "err", err)
	}
	// Iterate loading the different stats and updating the meters
	for i := 1; ; i++ {
		location1 := i % 2
		location2 := (i - 1) % 2

		runtime.ReadMemStats(memstats[location1])
		memAllocs.Mark(int64(memstats[location1].Mallocs - memstats[location2].Mallocs))
		memFrees.Mark(int64(memstats[location1].Frees - memstats[location2].Frees))
		memInuse.Mark(int64(memstats[location1].Alloc - memstats[location2].Alloc))
		memPauses.Mark(int64(memstats[location1].PauseTotalNs - memstats[location2].PauseTotalNs))

		if ReadDiskStats(diskstats[location1]) == nil {
			diskReads.Mark(diskstats[location1].ReadCount - diskstats[location2].ReadCount)
			diskReadBytes.Mark(diskstats[location1].ReadBytes - diskstats[location2].ReadBytes)
			diskWrites.Mark(diskstats[location1].WriteCount - diskstats[location2].WriteCount)
			diskWriteBytes.Mark(diskstats[location1].WriteBytes - diskstats[location2].WriteBytes)
		}
		time.Sleep(refresh)
	}
}
