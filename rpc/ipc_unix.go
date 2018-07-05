//
// (at your option) any later version.
//
//

// +build darwin dragonfly freebsd linux nacl netbsd openbsd solaris

package rpc

import (
	"context"
	"net"
	"os"
	"path/filepath"
)

func ipcListen(endpoint string) (net.Listener, error) {
	// Ensure the IPC path exists and remove any previous leftover
	if err := os.MkdirAll(filepath.Dir(endpoint), 0751); err != nil {
		return nil, err
	}
	os.Remove(endpoint)
	l, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, err
	}
	os.Chmod(endpoint, 0600)
	return l, nil
}

func newIPCConnection(ctx context.Context, endpoint string) (net.Conn, error) {
	return dialContext(ctx, "unix", endpoint)
}
