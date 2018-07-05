//
// (at your option) any later version.
//
//

package fdlimit

import "errors"

func Raise(max uint64) error {
	// This method is NOP by design:
	//  * Linux/Darwin counterparts need to manually increase per process limits
	//  * On Windows Go uses the CreateFile API, which is limited to 16K files, non
	//    changeable from within a running process
	// This way we can always "request" raising the limits, which will either have
	// or not have effect based on the platform we're running on.
	if max > 16384 {
		return errors.New("file descriptor limit (16384) reached")
	}
	return nil
}

func Current() (int, error) {
	// Please see Raise for the reason why we use hard coded 16K as the limit
	return 16384, nil
}

func Maximum() (int, error) {
	return Current()
}
