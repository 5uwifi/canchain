//
// (at your option) any later version.
//
//

/*
Package whisperv5 implements the Whisper protocol (version 5).

Whisper combines aspects of both DHTs and datagram messaging systems (e.g. UDP).
As such it may be likened and compared to both, not dissimilar to the
matter/energy duality (apologies to physicists for the blatant abuse of a
fundamental and beautiful natural principle).

Whisper is a pure identity-based messaging system. Whisper provides a low-level
(non-application-specific) but easily-accessible API without being based upon
or prejudiced by the low-level hardware attributes and characteristics,
particularly the notion of singular endpoints.
*/
package whisperv5

import (
	"fmt"
	"time"
)

const (
	EnvelopeVersion    = uint64(0)
	ProtocolVersion    = uint64(5)
	ProtocolVersionStr = "5.0"
	ProtocolName       = "shh"

	statusCode           = 0 // used by whisper protocol
	messagesCode         = 1 // normal whisper message
	p2pCode              = 2 // peer-to-peer message (to be consumed by the peer, but not forwarded any further)
	p2pRequestCode       = 3 // peer-to-peer message, used by Dapp protocol
	NumberOfMessageCodes = 64

	paddingMask   = byte(3)
	signatureFlag = byte(4)

	TopicLength     = 4
	signatureLength = 65
	aesKeyLength    = 32
	AESNonceLength  = 12
	keyIdSize       = 32

	MaxMessageSize        = uint32(10 * 1024 * 1024) // maximum accepted size of a message.
	DefaultMaxMessageSize = uint32(1024 * 1024)
	DefaultMinimumPoW     = 0.2

	padSizeLimit      = 256 // just an arbitrary number, could be changed without breaking the protocol (must not exceed 2^24)
	messageQueueLimit = 1024

	expirationCycle   = time.Second
	transmissionCycle = 300 * time.Millisecond

	DefaultTTL     = 50 // seconds
	SynchAllowance = 10 // seconds
)

type unknownVersionError uint64

func (e unknownVersionError) Error() string {
	return fmt.Sprintf("invalid envelope version %d", uint64(e))
}

type MailServer interface {
	Archive(env *Envelope)
	DeliverMail(whisperPeer *Peer, request *Envelope)
}
