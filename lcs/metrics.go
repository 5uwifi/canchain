package lcs

import (
	"github.com/5uwifi/canchain/lib/metrics"
	"github.com/5uwifi/canchain/lib/p2p"
)

var (
	/*	propTxnInPacketsMeter     = metrics.NewMeter("eth/prop/txns/in/packets")
		propTxnInTrafficMeter     = metrics.NewMeter("eth/prop/txns/in/traffic")
		propTxnOutPacketsMeter    = metrics.NewMeter("eth/prop/txns/out/packets")
		propTxnOutTrafficMeter    = metrics.NewMeter("eth/prop/txns/out/traffic")
		propHashInPacketsMeter    = metrics.NewMeter("eth/prop/hashes/in/packets")
		propHashInTrafficMeter    = metrics.NewMeter("eth/prop/hashes/in/traffic")
		propHashOutPacketsMeter   = metrics.NewMeter("eth/prop/hashes/out/packets")
		propHashOutTrafficMeter   = metrics.NewMeter("eth/prop/hashes/out/traffic")
		propBlockInPacketsMeter   = metrics.NewMeter("eth/prop/blocks/in/packets")
		propBlockInTrafficMeter   = metrics.NewMeter("eth/prop/blocks/in/traffic")
		propBlockOutPacketsMeter  = metrics.NewMeter("eth/prop/blocks/out/packets")
		propBlockOutTrafficMeter  = metrics.NewMeter("eth/prop/blocks/out/traffic")
		reqHashInPacketsMeter     = metrics.NewMeter("eth/req/hashes/in/packets")
		reqHashInTrafficMeter     = metrics.NewMeter("eth/req/hashes/in/traffic")
		reqHashOutPacketsMeter    = metrics.NewMeter("eth/req/hashes/out/packets")
		reqHashOutTrafficMeter    = metrics.NewMeter("eth/req/hashes/out/traffic")
		reqBlockInPacketsMeter    = metrics.NewMeter("eth/req/blocks/in/packets")
		reqBlockInTrafficMeter    = metrics.NewMeter("eth/req/blocks/in/traffic")
		reqBlockOutPacketsMeter   = metrics.NewMeter("eth/req/blocks/out/packets")
		reqBlockOutTrafficMeter   = metrics.NewMeter("eth/req/blocks/out/traffic")
		reqHeaderInPacketsMeter   = metrics.NewMeter("eth/req/headers/in/packets")
		reqHeaderInTrafficMeter   = metrics.NewMeter("eth/req/headers/in/traffic")
		reqHeaderOutPacketsMeter  = metrics.NewMeter("eth/req/headers/out/packets")
		reqHeaderOutTrafficMeter  = metrics.NewMeter("eth/req/headers/out/traffic")
		reqBodyInPacketsMeter     = metrics.NewMeter("eth/req/bodies/in/packets")
		reqBodyInTrafficMeter     = metrics.NewMeter("eth/req/bodies/in/traffic")
		reqBodyOutPacketsMeter    = metrics.NewMeter("eth/req/bodies/out/packets")
		reqBodyOutTrafficMeter    = metrics.NewMeter("eth/req/bodies/out/traffic")
		reqStateInPacketsMeter    = metrics.NewMeter("eth/req/states/in/packets")
		reqStateInTrafficMeter    = metrics.NewMeter("eth/req/states/in/traffic")
		reqStateOutPacketsMeter   = metrics.NewMeter("eth/req/states/out/packets")
		reqStateOutTrafficMeter   = metrics.NewMeter("eth/req/states/out/traffic")
		reqReceiptInPacketsMeter  = metrics.NewMeter("eth/req/receipts/in/packets")
		reqReceiptInTrafficMeter  = metrics.NewMeter("eth/req/receipts/in/traffic")
		reqReceiptOutPacketsMeter = metrics.NewMeter("eth/req/receipts/out/packets")
		reqReceiptOutTrafficMeter = metrics.NewMeter("eth/req/receipts/out/traffic")*/
	miscInPacketsMeter  = metrics.NewRegisteredMeter("les/misc/in/packets", nil)
	miscInTrafficMeter  = metrics.NewRegisteredMeter("les/misc/in/traffic", nil)
	miscOutPacketsMeter = metrics.NewRegisteredMeter("les/misc/out/packets", nil)
	miscOutTrafficMeter = metrics.NewRegisteredMeter("les/misc/out/traffic", nil)
)

type meteredMsgReadWriter struct {
	p2p.MsgReadWriter
	version int
}

func newMeteredMsgWriter(rw p2p.MsgReadWriter) p2p.MsgReadWriter {
	if !metrics.Enabled {
		return rw
	}
	return &meteredMsgReadWriter{MsgReadWriter: rw}
}

func (rw *meteredMsgReadWriter) Init(version int) {
	rw.version = version
}

func (rw *meteredMsgReadWriter) ReadMsg() (p2p.Msg, error) {
	msg, err := rw.MsgReadWriter.ReadMsg()
	if err != nil {
		return msg, err
	}
	packets, traffic := miscInPacketsMeter, miscInTrafficMeter
	packets.Mark(1)
	traffic.Mark(int64(msg.Size))

	return msg, err
}

func (rw *meteredMsgReadWriter) WriteMsg(msg p2p.Msg) error {
	packets, traffic := miscOutPacketsMeter, miscOutTrafficMeter
	packets.Mark(1)
	traffic.Mark(int64(msg.Size))

	return rw.MsgReadWriter.WriteMsg(msg)
}
