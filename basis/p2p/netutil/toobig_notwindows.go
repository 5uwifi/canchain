//
// (at your option) any later version.
//
//

//+build !windows

package netutil

func isPacketTooBig(err error) bool {
	return false
}
