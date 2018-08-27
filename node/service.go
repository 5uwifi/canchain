package node

import (
	"reflect"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/candb"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/rpc"
)

type ServiceContext struct {
	config         *Config
	services       map[reflect.Type]Service
	EventMux       *event.TypeMux
	AccountManager *accounts.Manager
}

func (ctx *ServiceContext) OpenDatabase(name string, cache int, handles int) (candb.Database, error) {
	if ctx.config.DataDir == "" {
		return candb.NewMemDatabase(), nil
	}
	db, err := candb.NewLDBDatabase(ctx.config.ResolvePath(name), cache, handles)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func (ctx *ServiceContext) ResolvePath(path string) string {
	return ctx.config.ResolvePath(path)
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

type Service interface {
	Protocols() []p2p.Protocol

	APIs() []rpc.API

	Start(server *p2p.Server) error

	Stop() error
}
