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
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/rlp"
)

type ledgerOpcode byte

type ledgerParam1 byte

type ledgerParam2 byte

const (
	ledgerOpRetrieveAddress  ledgerOpcode = 0x02
	ledgerOpSignTransaction  ledgerOpcode = 0x04
	ledgerOpGetConfiguration ledgerOpcode = 0x06

	ledgerP1DirectlyFetchAddress    ledgerParam1 = 0x00
	ledgerP1InitTransactionData     ledgerParam1 = 0x00
	ledgerP1ContTransactionData     ledgerParam1 = 0x80
	ledgerP2DiscardAddressChainCode ledgerParam2 = 0x00
)

var errLedgerReplyInvalidHeader = errors.New("ledger: invalid reply header")

var errLedgerInvalidVersionReply = errors.New("ledger: invalid version reply")

type ledgerDriver struct {
	device  io.ReadWriter
	version [3]byte
	browser bool
	failure error
	log     log4j.Logger
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
		return "CANChain app in browser mode", w.failure
	}
	if w.offline() {
		return "CANChain app offline", w.failure
	}
	return fmt.Sprintf("CANChain app v%d.%d.%d online", w.version[0], w.version[1], w.version[2]), w.failure
}

func (w *ledgerDriver) offline() bool {
	return w.version == [3]byte{0, 0, 0}
}

func (w *ledgerDriver) Open(device io.ReadWriter, passphrase string) error {
	w.device, w.failure = device, nil

	_, err := w.ledgerDerive(accounts.DefaultBaseDerivationPath)
	if err != nil {
		if err == errLedgerReplyInvalidHeader {
			w.browser = true
		}
		return nil
	}
	if w.version, err = w.ledgerVersion(); err != nil {
		w.version = [3]byte{1, 0, 0}
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

func (w *ledgerDriver) SignTx(path accounts.DerivationPath, tx *types.Transaction, chainID *big.Int) (common.Address, *types.Transaction, error) {
	if w.offline() {
		return common.Address{}, nil, accounts.ErrWalletClosed
	}
	if chainID != nil && w.version[0] <= 1 && w.version[1] <= 0 && w.version[2] <= 2 {
		return common.Address{}, nil, fmt.Errorf("Ledger v%d.%d.%d doesn't support signing this transaction, please update to v1.0.3 at least", w.version[0], w.version[1], w.version[2])
	}
	return w.ledgerSign(path, tx, chainID)
}

func (w *ledgerDriver) ledgerVersion() ([3]byte, error) {
	reply, err := w.ledgerExchange(ledgerOpGetConfiguration, 0, 0, nil)
	if err != nil {
		return [3]byte{}, err
	}
	if len(reply) != 4 {
		return [3]byte{}, errLedgerInvalidVersionReply
	}
	var version [3]byte
	copy(version[:], reply[1:])
	return version, nil
}

func (w *ledgerDriver) ledgerDerive(derivationPath []uint32) (common.Address, error) {
	path := make([]byte, 1+4*len(derivationPath))
	path[0] = byte(len(derivationPath))
	for i, component := range derivationPath {
		binary.BigEndian.PutUint32(path[1+4*i:], component)
	}
	reply, err := w.ledgerExchange(ledgerOpRetrieveAddress, ledgerP1DirectlyFetchAddress, ledgerP2DiscardAddressChainCode, path)
	if err != nil {
		return common.Address{}, err
	}
	if len(reply) < 1 || len(reply) < 1+int(reply[0]) {
		return common.Address{}, errors.New("reply lacks public key entry")
	}
	reply = reply[1+int(reply[0]):]

	if len(reply) < 1 || len(reply) < 1+int(reply[0]) {
		return common.Address{}, errors.New("reply lacks address entry")
	}
	hexstr := reply[1 : 1+int(reply[0])]

	var address common.Address
	hex.Decode(address[:], hexstr)
	return address, nil
}

func (w *ledgerDriver) ledgerSign(derivationPath []uint32, tx *types.Transaction, chainID *big.Int) (common.Address, *types.Transaction, error) {
	path := make([]byte, 1+4*len(derivationPath))
	path[0] = byte(len(derivationPath))
	for i, component := range derivationPath {
		binary.BigEndian.PutUint32(path[1+4*i:], component)
	}
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

	var (
		op    = ledgerP1InitTransactionData
		reply []byte
	)
	for len(payload) > 0 {
		chunk := 255
		if chunk > len(payload) {
			chunk = len(payload)
		}
		reply, err = w.ledgerExchange(ledgerOpSignTransaction, op, 0, payload[:chunk])
		if err != nil {
			return common.Address{}, nil, err
		}
		payload = payload[chunk:]
		op = ledgerP1ContTransactionData
	}
	if len(reply) != 65 {
		return common.Address{}, nil, errors.New("reply lacks signature")
	}
	signature := append(reply[1:], reply[0])

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

func (w *ledgerDriver) ledgerExchange(opcode ledgerOpcode, p1 ledgerParam1, p2 ledgerParam2, data []byte) ([]byte, error) {
	apdu := make([]byte, 2, 7+len(data))

	binary.BigEndian.PutUint16(apdu, uint16(5+len(data)))
	apdu = append(apdu, []byte{0xe0, byte(opcode), byte(p1), byte(p2), byte(len(data))}...)
	apdu = append(apdu, data...)

	header := []byte{0x01, 0x01, 0x05, 0x00, 0x00}
	chunk := make([]byte, 64)
	space := len(chunk) - len(header)

	for i := 0; len(apdu) > 0; i++ {
		chunk = append(chunk[:0], header...)
		binary.BigEndian.PutUint16(chunk[3:], uint16(i))

		if len(apdu) > space {
			chunk = append(chunk, apdu[:space]...)
			apdu = apdu[space:]
		} else {
			chunk = append(chunk, apdu...)
			apdu = nil
		}
		w.log.Trace("Data chunk sent to the Ledger", "chunk", hexutil.Bytes(chunk))
		if _, err := w.device.Write(chunk); err != nil {
			return nil, err
		}
	}
	var reply []byte
	chunk = chunk[:64]
	for {
		if _, err := io.ReadFull(w.device, chunk); err != nil {
			return nil, err
		}
		w.log.Trace("Data chunk received from the Ledger", "chunk", hexutil.Bytes(chunk))

		if chunk[0] != 0x01 || chunk[1] != 0x01 || chunk[2] != 0x05 {
			return nil, errLedgerReplyInvalidHeader
		}
		var payload []byte

		if chunk[3] == 0x00 && chunk[4] == 0x00 {
			reply = make([]byte, 0, int(binary.BigEndian.Uint16(chunk[5:7])))
			payload = chunk[7:]
		} else {
			payload = chunk[5:]
		}
		if left := cap(reply) - len(reply); left > len(payload) {
			reply = append(reply, payload...)
		} else {
			reply = append(reply, payload[:left]...)
			break
		}
	}
	return reply[:len(reply)-2], nil
}
