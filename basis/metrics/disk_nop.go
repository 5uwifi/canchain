//
// (at your option) any later version.
//
//

// +build !linux

package metrics

import "errors"

func ReadDiskStats(stats *DiskStats) error {
	return errors.New("Not implemented")
}
