//
// (at your option) any later version.
//
//

package rpc

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"

	"github.com/5uwifi/canchain/common/hexutil"
	"gopkg.in/fatih/set.v0"
)

type API struct {
	Namespace string      // namespace under which the rpc methods of Service are exposed
	Version   string      // api version for DApp's
	Service   interface{} // receiver instance which holds the methods
	Public    bool        // indication if the methods must be considered safe for public use
}

type callback struct {
	rcvr        reflect.Value  // receiver of method
	method      reflect.Method // callback
	argTypes    []reflect.Type // input argument types
	hasCtx      bool           // method's first argument is a context (not included in argTypes)
	errPos      int            // err return idx, of -1 when method cannot return error
	isSubscribe bool           // indication if the callback is a subscription
}

type service struct {
	name          string        // name for service
	typ           reflect.Type  // receiver type
	callbacks     callbacks     // registered handlers
	subscriptions subscriptions // available subscriptions/notifications
}

type serverRequest struct {
	id            interface{}
	svcname       string
	callb         *callback
	args          []reflect.Value
	isUnsubscribe bool
	err           Error
}

type serviceRegistry map[string]*service // collection of services
type callbacks map[string]*callback      // collection of RPC callbacks
type subscriptions map[string]*callback  // collection of subscription callbacks

type Server struct {
	services serviceRegistry

	run      int32
	codecsMu sync.Mutex
	codecs   *set.Set
}

type rpcRequest struct {
	service  string
	method   string
	id       interface{}
	isPubSub bool
	params   interface{}
	err      Error // invalid batch element
}

type Error interface {
	Error() string  // returns the message
	ErrorCode() int // returns the code
}

type ServerCodec interface {
	// Read next request
	ReadRequestHeaders() ([]rpcRequest, bool, Error)
	// Parse request argument to the given types
	ParseRequestArguments(argTypes []reflect.Type, params interface{}) ([]reflect.Value, Error)
	// Assemble success response, expects response id and payload
	CreateResponse(id interface{}, reply interface{}) interface{}
	// Assemble error response, expects response id and error
	CreateErrorResponse(id interface{}, err Error) interface{}
	// Assemble error response with extra information about the error through info
	CreateErrorResponseWithInfo(id interface{}, err Error, info interface{}) interface{}
	// Create notification response
	CreateNotification(id, namespace string, event interface{}) interface{}
	// Write msg to client.
	Write(msg interface{}) error
	// Close underlying data stream
	Close()
	// Closed when underlying connection is closed
	Closed() <-chan interface{}
}

type BlockNumber int64

const (
	PendingBlockNumber  = BlockNumber(-2)
	LatestBlockNumber   = BlockNumber(-1)
	EarliestBlockNumber = BlockNumber(0)
)

// - "latest", "earliest" or "pending" as string arguments
// - the block number
// - an invalid block number error when the given argument isn't a known strings
// - an out of range error when the given block number is either too little or too large
func (bn *BlockNumber) UnmarshalJSON(data []byte) error {
	input := strings.TrimSpace(string(data))
	if len(input) >= 2 && input[0] == '"' && input[len(input)-1] == '"' {
		input = input[1 : len(input)-1]
	}

	switch input {
	case "earliest":
		*bn = EarliestBlockNumber
		return nil
	case "latest":
		*bn = LatestBlockNumber
		return nil
	case "pending":
		*bn = PendingBlockNumber
		return nil
	}

	blckNum, err := hexutil.DecodeUint64(input)
	if err != nil {
		return err
	}
	if blckNum > math.MaxInt64 {
		return fmt.Errorf("Blocknumber too high")
	}

	*bn = BlockNumber(blckNum)
	return nil
}

func (bn BlockNumber) Int64() int64 {
	return (int64)(bn)
}
