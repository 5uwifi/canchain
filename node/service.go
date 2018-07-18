
package node

import (
	"reflect"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/basis/event"
	"github.com/5uwifi/canchain/basis/p2p"
	"github.com/5uwifi/canchain/rpc"
)

type ServiceContext struct {
	config         *Config
	services       map[reflect.Type]Service // Index of the already constructed services
	EventMux       *event.TypeMux           // Event multiplexer used for decoupled notifications
	AccountManager *accounts.Manager        // Account manager created by the node.
}

func (ctx *ServiceContext) OpenDatabase(name string, cache int, handles int) (candb.Database, error) {
	if ctx.config.DataDir == "" {
		return candb.NewMemDatabase(), nil
	}
	db, err := candb.NewLDBDatabase(ctx.config.resolvePath(name), cache, handles)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func (ctx *ServiceContext) ResolvePath(path string) string {
	return ctx.config.resolvePath(path)
}

func (ctx *ServiceContext) Service(service interface{}) error {
	element := reflect.ValueOf(service).Elem()
	if running, ok := ctx.services[element.Type()]; ok {
		element.Set(reflect.ValueOf(running))
		return nil
	}
	return ErrServiceUnknown
}

type ServiceConstructor func(ctx *ServiceContext) (Service, error)

//
//
// • Service life-cycle management is delegated to the node. The service is allowed to
//
// • Restart logic is not required as the node will create a fresh instance
type Service interface {
	// Protocols retrieves the P2P protocols the service wishes to start.
	Protocols() []p2p.Protocol

	// APIs retrieves the list of RPC descriptors the service provides
	APIs() []rpc.API

	// Start is called after all services have been constructed and the networking
	// layer was also initialized to spawn any goroutines required by the service.
	Start(server *p2p.Server) error

	// Stop terminates all goroutines belonging to the service, blocking until they
	// are all terminated.
	Stop() error
}
