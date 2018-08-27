
//go:generate protoc --go_out=import_path=trezor:. types.proto messages.proto

package trezor

import (
	"reflect"

	"github.com/golang/protobuf/proto"
)

func Type(msg proto.Message) uint16 {
	return uint16(MessageType_value["MessageType_"+reflect.TypeOf(msg).Elem().Name()])
}

func Name(kind uint16) string {
	name := MessageType_name[int32(kind)]
	if len(name) < 12 {
		return name
	}
	return name[12:]
}
