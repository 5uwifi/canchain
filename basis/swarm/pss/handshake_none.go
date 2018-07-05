//
// (at your option) any later version.
//
//

// +build nopsshandshake

package pss

const (
	IsActiveHandshake = false
)

func NewHandshakeParams() interface{} {
	return nil
}
