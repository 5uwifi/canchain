package rpc

import (
	"net"

	"github.com/5uwifi/canchain/lib/log4j"
)

func StartHTTPEndpoint(endpoint string, apis []API, modules []string, cors []string, vhosts []string, timeouts HTTPTimeouts) (net.Listener, *Server, error) {
	whitelist := make(map[string]bool)
	for _, module := range modules {
		whitelist[module] = true
	}
	handler := NewServer()
	for _, api := range apis {
		if whitelist[api.Namespace] || (len(whitelist) == 0 && api.Public) {
			if err := handler.RegisterName(api.Namespace, api.Service); err != nil {
				return nil, nil, err
			}
			log4j.Debug("HTTP registered", "namespace", api.Namespace)
		}
	}
	var (
		listener net.Listener
		err      error
	)
	if listener, err = net.Listen("tcp", endpoint); err != nil {
		return nil, nil, err
	}
	go NewHTTPServer(cors, vhosts, timeouts, handler).Serve(listener)
	return listener, handler, err
}

func StartWSEndpoint(endpoint string, apis []API, modules []string, wsOrigins []string, exposeAll bool) (net.Listener, *Server, error) {

	whitelist := make(map[string]bool)
	for _, module := range modules {
		whitelist[module] = true
	}
	handler := NewServer()
	for _, api := range apis {
		if exposeAll || whitelist[api.Namespace] || (len(whitelist) == 0 && api.Public) {
			if err := handler.RegisterName(api.Namespace, api.Service); err != nil {
				return nil, nil, err
			}
			log4j.Debug("WebSocket registered", "service", api.Service, "namespace", api.Namespace)
		}
	}
	var (
		listener net.Listener
		err      error
	)
	if listener, err = net.Listen("tcp", endpoint); err != nil {
		return nil, nil, err
	}
	go NewWSServer(wsOrigins, handler).Serve(listener)
	return listener, handler, err

}

func StartIPCEndpoint(ipcEndpoint string, apis []API) (net.Listener, *Server, error) {
	handler := NewServer()
	for _, api := range apis {
		if err := handler.RegisterName(api.Namespace, api.Service); err != nil {
			return nil, nil, err
		}
		log4j.Debug("IPC registered", "namespace", api.Namespace)
	}
	listener, err := ipcListen(ipcEndpoint)
	if err != nil {
		return nil, nil, err
	}
	go handler.ServeListener(listener)
	return listener, handler, nil
}
