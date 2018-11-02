// +build js

package rpc

import (
	"context"
	"errors"
	"net"
)

var errNotSupported = errors.New("rpc: not supported")

func ipcListen(endpoint string) (net.Listener, error) {
	return nil, errNotSupported
}

func newIPCConnection(ctx context.Context, endpoint string) (net.Conn, error) {
	return nil, errNotSupported
}
