//
// (at your option) any later version.
//
//


package usbwallet

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/5uwifi/canchain/accounts"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/rlp"
)

type ledgerOpcode byte

type ledgerParam1 byte

type ledgerParam2 byte

const (
	ledgerOpRetrieveAddress  ledgerOpcode = 0x02 // Returns the public key and Ethereum address for a given BIP 32 path
	ledgerOpSignTransaction  ledgerOpcode = 0x04 // Signs an Ethereum transaction after having the user validate the parameters
	ledgerOpGetConfiguration ledgerOpcode = 0x06 // Returns specific wallet application configuration

	ledgerP1DirectlyFetchAddress    ledgerParam1 = 0x00 // Return address directly from the wallet
	ledgerP1InitTransactionData     ledgerParam1 = 0x00 // First transaction data block for signing
	ledgerP1ContTransactionData     ledgerParam1 = 0x80 // Subsequent transaction data block for signing
	ledgerP2DiscardAddressChainCode ledgerParam2 = 0x00 // Do not return the chain code along with the address
)

var errLedgerReplyInvalidHeader = errors.New("ledger: invalid reply header")

var errLedgerInvalidVersionReply = errors.New("ledger: invalid version reply")

type ledgerDriver struct {
	device  io.ReadWriter // USB device connection to communicate through
	version [3]byte       // Current version of the Ledger firmware (zero if app is offline)
	browser bool          // Flag whether the Ledger is in browser mode (reply channel mismatch)
	failure error         // Any failure that would make the device unusable
	log     log4j.Logger  // Contextual logger to tag the ledger with its id
}

func newLedgerDriver(logger log4j.Logger) driver {
	return &ledgerDriver{
		log: logger,
	}
}

func (w *ledgerDriver) Status() (string, error) {
	if w.failure != nil {
		return fmt.Sprintf("Failed: %v", w.failure), w.failure
	}
	if w.browser {
		return "Ethereum app in browser mode", w.failure
	}
	if w.offline() {
		return "Ethereum app offline", w.failure
	}
	return fmt.Sprintf("Ethereum app v%d.%d.%d online", w.version[0], w.version[1], w.version[2]), w.failure
}

//
func (w *ledgerDriver) offline() bool {
	return w.version == [3]byte{0, 0, 0}
}

func (w *ledgerDriver) Open(device io.ReadWriter, passphrase string) error {
	w.device, w.failure = device, nil

	_, err := w.ledgerDerive(accounts.DefaultBaseDerivationPath)
	if err != nil {
		// Ethereum app is not running or in browser mode, nothing more to do, return
		if err == errLedgerReplyInvalidHeader {
			w.browser = true
		}
		return nil
	}
	// Try to resolve the Ethereum app's version, will fail prior to v1.0.2
	if w.version, err = w.ledgerVersion(); err != nil {
		w.version = [3]byte{1, 0, 0} // Assume worst case, can't verify if v1.0.0 or v1.0.1
	}
	return nil
}

func (w *ledgerDriver) Close() error {
	w.browser, w.version = false, [3]byte{}
	return nil
}

func (w *ledgerDriver) Heartbeat() error {
	if _, err := w.ledgerVersion(); err != nil && err != errLedgerInvalidVersionReply {
		w.failure = err
		return err
	}
	return nil
}

func (w *ledgerDriver) Derive(path accounts.DerivationPath) (common.Address, error) {
	return w.ledgerDerive(path)
}

//
func (w *ledgerDriver) SignTx(path accounts.DerivationPath, tx *types.Transaction, chainID *big.Int) (common.Address, *types.Transaction, error) {
	// If the Ethereum app doesn't run, abort
	if w.offline() {
		return common.Address{}, nil, accounts.ErrWalletClosed
	}
	// Ensure the wallet is capable of signing the given transaction
	if chainID != nil && w.version[0] <= 1 && w.version[1] <= 0 && w.version[2] <= 2 {
		return common.Address{}, nil, fmt.Errorf("Ledger v%d.%d.%d doesn't support signing this transaction, please update to v1.0.3 at least", w.version[0], w.version[1], w.version[2])
	}
	// All infos gathered and metadata checks out, request signing
	return w.ledgerSign(path, tx, chainID)
}

//
//
//   ----+-----+----+----+----+---
//
//
//   ---------------------------------------------------+--------
func (w *ledgerDriver) ledgerVersion() ([3]byte, error) {
	// Send the request and wait for the response
	reply, err := w.ledgerExchange(ledgerOpGetConfiguration, 0, 0, nil)
	if err != nil {
		return [3]byte{}, err
	}
	if len(reply) != 4 {
		return [3]byte{}, errLedgerInvalidVersionReply
	}
	// Cache the version for future reference
	var version [3]byte
	copy(version[:], reply[1:])
	return version, nil
}

//
//
//   ----+-----+----+----+-----+---
//                  | 00: do not return the chain code
//                  | 01: return the chain code
//                       | var | 00
//
//
//   -------------------------------------------------+--------
//   ...                                              | 4 bytes
//
//
//   ------------------------+-------------------
func (w *ledgerDriver) ledgerDerive(derivationPath []uint32) (common.Address, error) {
	// Flatten the derivation path into the Ledger request
	path := make([]byte, 1+4*len(derivationPath))
	path[0] = byte(len(derivationPath))
	for i, component := range derivationPath {
		binary.BigEndian.PutUint32(path[1+4*i:], component)
	}
	// Send the request and wait for the response
	reply, err := w.ledgerExchange(ledgerOpRetrieveAddress, ledgerP1DirectlyFetchAddress, ledgerP2DiscardAddressChainCode, path)
	if err != nil {
		return common.Address{}, err
	}
	// Discard the public key, we don't need that for now
	if len(reply) < 1 || len(reply) < 1+int(reply[0]) {
		return common.Address{}, errors.New("reply lacks public key entry")
	}
	reply = reply[1+int(reply[0]):]

	// Extract the Ethereum hex address string
	if len(reply) < 1 || len(reply) < 1+int(reply[0]) {
		return common.Address{}, errors.New("reply lacks address entry")
	}
	hexstr := reply[1 : 1+int(reply[0])]

	// Decode the hex sting into an Ethereum address and return
	var address common.Address
	hex.Decode(address[:], hexstr)
	return address, nil
}

//
//
//   ----+-----+----+----+-----+---
//                  | 00 | variable | variable
//
//
//   -------------------------------------------------+----------
//   ...                                              | 4 bytes
//
//
//   ----------------------+----------
//
//
//   ------------+---------
func (w *ledgerDriver) ledgerSign(derivationPath []uint32, tx *types.Transaction, chainID *big.Int) (common.Address, *types.Transaction, error) {
	// Flatten the derivation path into the Ledger request
	path := make([]byte, 1+4*len(derivationPath))
	path[0] = byte(len(derivationPath))
	for i, component := range derivationPath {
		binary.BigEndian.PutUint32(path[1+4*i:], component)
	}
	// Create the transaction RLP based on whether legacy or EIP155 signing was requested
	var (
		txrlp []byte
		err   error
	)
	if chainID == nil {
		if txrlp, err = rlp.EncodeToBytes([]interface{}{tx.Nonce(), tx.GasPrice(), tx.Gas(), tx.To(), tx.Value(), tx.Data()}); err != nil {
			return common.Address{}, nil, err
		}
	} else {
		if txrlp, err = rlp.EncodeToBytes([]interface{}{tx.Nonce(), tx.GasPrice(), tx.Gas(), tx.To(), tx.Value(), tx.Data(), chainID, big.NewInt(0), big.NewInt(0)}); err != nil {
			return common.Address{}, nil, err
		}
	}
	payload := append(path, txrlp...)

	// Send the request and wait for the response
	var (
		op    = ledgerP1InitTransactionData
		reply []byte
	)
	for len(payload) > 0 {
		// Calculate the size of the next data chunk
		chunk := 255
		if chunk > len(payload) {
			chunk = len(payload)
		}
		// Send the chunk over, ensuring it's processed correctly
		reply, err = w.ledgerExchange(ledgerOpSignTransaction, op, 0, payload[:chunk])
		if err != nil {
			return common.Address{}, nil, err
		}
		// Shift the payload and ensure subsequent chunks are marked as such
		payload = payload[chunk:]
		op = ledgerP1ContTransactionData
	}
	// Extract the Ethereum signature and do a sanity validation
	if len(reply) != 65 {
		return common.Address{}, nil, errors.New("reply lacks signature")
	}
	signature := append(reply[1:], reply[0])

	// Create the correct signer and signature transform based on the chain ID
	var signer types.Signer
	if chainID == nil {
		signer = new(types.HomesteadSigner)
	} else {
		signer = types.NewEIP155Signer(chainID)
		signature[64] = signature[64] - byte(chainID.Uint64()*2+35)
	}
	signed, err := tx.WithSignature(signer, signature)
	if err != nil {
		return common.Address{}, nil, err
	}
	sender, err := types.Sender(signer, signed)
	if err != nil {
		return common.Address{}, nil, err
	}
	return sender, signed, nil
}

//
//
//  --------------------------------------+----------
//
//
//
//
//
//  -----------------------------------
func (w *ledgerDriver) ledgerExchange(opcode ledgerOpcode, p1 ledgerParam1, p2 ledgerParam2, data []byte) ([]byte, error) {
	// Construct the message payload, possibly split into multiple chunks
	apdu := make([]byte, 2, 7+len(data))

	binary.BigEndian.PutUint16(apdu, uint16(5+len(data)))
	apdu = append(apdu, []byte{0xe0, byte(opcode), byte(p1), byte(p2), byte(len(data))}...)
	apdu = append(apdu, data...)

	// Stream all the chunks to the device
	header := []byte{0x01, 0x01, 0x05, 0x00, 0x00} // Channel ID and command tag appended
	chunk := make([]byte, 64)
	space := len(chunk) - len(header)

	for i := 0; len(apdu) > 0; i++ {
		// Construct the new message to stream
		chunk = append(chunk[:0], header...)
		binary.BigEndian.PutUint16(chunk[3:], uint16(i))

		if len(apdu) > space {
			chunk = append(chunk, apdu[:space]...)
			apdu = apdu[space:]
		} else {
			chunk = append(chunk, apdu...)
			apdu = nil
		}
		// Send over to the device
		w.log.Trace("Data chunk sent to the Ledger", "chunk", hexutil.Bytes(chunk))
		if _, err := w.device.Write(chunk); err != nil {
			return nil, err
		}
	}
	// Stream the reply back from the wallet in 64 byte chunks
	var reply []byte
	chunk = chunk[:64] // Yeah, we surely have enough space
	for {
		// Read the next chunk from the Ledger wallet
		if _, err := io.ReadFull(w.device, chunk); err != nil {
			return nil, err
		}
		w.log.Trace("Data chunk received from the Ledger", "chunk", hexutil.Bytes(chunk))

		// Make sure the transport header matches
		if chunk[0] != 0x01 || chunk[1] != 0x01 || chunk[2] != 0x05 {
			return nil, errLedgerReplyInvalidHeader
		}
		// If it's the first chunk, retrieve the total message length
		var payload []byte

		if chunk[3] == 0x00 && chunk[4] == 0x00 {
			reply = make([]byte, 0, int(binary.BigEndian.Uint16(chunk[5:7])))
			payload = chunk[7:]
		} else {
			payload = chunk[5:]
		}
		// Append to the reply and stop when filled up
		if left := cap(reply) - len(reply); left > len(payload) {
			reply = append(reply, payload...)
		} else {
			reply = append(reply, payload[:left]...)
			break
		}
	}
	return reply[:len(reply)-2], nil
}
