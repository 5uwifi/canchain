package discover

import (
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/5uwifi/canchain/lib/log4j"
)

const (
	ntpPool   = "pool.ntp.org"
	ntpChecks = 3
)

type durationSlice []time.Duration

func (s durationSlice) Len() int           { return len(s) }
func (s durationSlice) Less(i, j int) bool { return s[i] < s[j] }
func (s durationSlice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func checkClockDrift() {
	drift, err := sntpDrift(ntpChecks)
	if err != nil {
		return
	}
	if drift < -driftThreshold || drift > driftThreshold {
		log4j.Warn(fmt.Sprintf("System clock seems off by %v, which can prevent network connectivity", drift))
		log4j.Warn("Please enable network time synchronisation in system settings.")
	} else {
		log4j.Debug("NTP sanity check done", "drift", drift)
	}
}

func sntpDrift(measurements int) (time.Duration, error) {
	addr, err := net.ResolveUDPAddr("udp", ntpPool+":123")
	if err != nil {
		return 0, err
	}
	request := make([]byte, 48)
	request[0] = 3<<3 | 3

	drifts := []time.Duration{}
	for i := 0; i < measurements+2; i++ {
		conn, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			return 0, err
		}
		defer conn.Close()

		sent := time.Now()
		if _, err = conn.Write(request); err != nil {
			return 0, err
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))

		reply := make([]byte, 48)
		if _, err = conn.Read(reply); err != nil {
			return 0, err
		}
		elapsed := time.Since(sent)

		sec := uint64(reply[43]) | uint64(reply[42])<<8 | uint64(reply[41])<<16 | uint64(reply[40])<<24
		frac := uint64(reply[47]) | uint64(reply[46])<<8 | uint64(reply[45])<<16 | uint64(reply[44])<<24

		nanosec := sec*1e9 + (frac*1e9)>>32

		t := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(nanosec)).Local()

		drifts = append(drifts, sent.Sub(t)+elapsed/2)
	}
	sort.Sort(durationSlice(drifts))

	drift := time.Duration(0)
	for i := 1; i < len(drifts)-1; i++ {
		drift += drifts[i]
	}
	return drift / time.Duration(measurements), nil
}
