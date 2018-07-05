//
// (at your option) any later version.
//
//

package downloader

type DoneEvent struct{}
type StartEvent struct{}
type FailedEvent struct{ Err error }
