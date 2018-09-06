package lcs

import (
	"github.com/5uwifi/canchain/lib/metrics"
	"github.com/5uwifi/canchain/lib/p2p"
)

var (
	/*	propTxnInPacketsMeter     = metrics.NewMeter("can/prop/txns/in/packets")
		propTxnInTrafficMeter     = metrics.NewMeter("can/prop/txns/in/traffic")
		propTxnOutPacketsMeter    = metrics.NewMeter("can/prop/txns/out/packets")
		propTxnOutTrafficMeter    = metrics.NewMeter("can/prop/txns/out/traffic")
		propHashInPacketsMeter    = metrics.NewMeter("can/prop/hashes/in/packets")
		propHashInTrafficMeter    = metrics.NewMeter("can/prop/hashes/in/traffic")
		propHashOutPacketsMeter   = metrics.NewMeter("can/prop/hashes/out/packets")
		propHashOutTrafficMeter   = metrics.NewMeter("can/prop/hashes/out/traffic")
		propBlockInPacketsMeter   = metrics.NewMeter("can/prop/blocks/in/packets")
		propBlockInTrafficMeter   = metrics.NewMeter("can/prop/blocks/in/traffic")
		propBlockOutPacketsMeter  = metrics.NewMeter("can/prop/blocks/out/packets")
		propBlockOutTrafficMeter  = metrics.NewMeter("can/prop/blocks/out/traffic")
		reqHashInPacketsMeter     = metrics.NewMeter("can/req/hashes/in/packets")
		reqHashInTrafficMeter     = metrics.NewMeter("can/req/hashes/in/traffic")
		reqHashOutPacketsMeter    = metrics.NewMeter("can/req/hashes/out/packets")
		reqHashOutTrafficMeter    = metrics.NewMeter("can/req/hashes/out/traffic")
		reqBlockInPacketsMeter    = metrics.NewMeter("can/req/blocks/in/packets")
		reqBlockInTrafficMeter    = metrics.NewMeter("can/req/blocks/in/traffic")
		reqBlockOutPacketsMeter   = metrics.NewMeter("can/req/blocks/out/packets")
		reqBlockOutTrafficMeter   = metrics.NewMeter("can/req/blocks/out/traffic")
		reqHeaderInPacketsMeter   = metrics.NewMeter("can/req/headers/in/packets")
		reqHeaderInTrafficMeter   = metrics.NewMeter("can/req/headers/in/traffic")
		reqHeaderOutPacketsMeter  = metrics.NewMeter("can/req/headers/out/packets")
		reqHeaderOutTrafficMeter  = metrics.NewMeter("can/req/headers/out/traffic")
		reqBodyInPacketsMeter     = metrics.NewMeter("can/req/bodies/in/packets")
		reqBodyInTrafficMeter     = metrics.NewMeter("can/req/bodies/in/traffic")
		reqBodyOutPacketsMeter    = metrics.NewMeter("can/req/bodies/out/packets")
		reqBodyOutTrafficMeter    = metrics.NewMeter("can/req/bodies/out/traffic")
		reqStateInPacketsMeter    = metrics.NewMeter("can/req/states/in/packets")
		reqStateInTrafficMeter    = metrics.NewMeter("can/req/states/in/traffic")
		reqStateOutPacketsMeter   = metrics.NewMeter("can/req/states/out/packets")
		reqStateOutTrafficMeter   = metrics.NewMeter("can/req/states/out/traffic")
		reqReceiptInPacketsMeter  = metrics.NewMeter("can/req/receipts/in/packets")
		reqReceiptInTrafficMeter  = metrics.NewMeter("can/req/receipts/in/traffic")
		reqReceiptOutPacketsMeter = metrics.NewMeter("can/req/receipts/out/packets")
		reqReceiptOutTrafficMeter = metrics.NewMeter("can/req/receipts/out/traffic")*/
	miscInPacketsMeter  = metrics.NewRegisteredMeter("lcs/misc/in/packets", nil)
	miscInTrafficMeter  = metrics.NewRegisteredMeter("lcs/misc/in/traffic", nil)
	miscOutPacketsMeter = metrics.NewRegisteredMeter("lcs/misc/out/packets", nil)
	miscOutTrafficMeter = metrics.NewRegisteredMeter("lcs/misc/out/traffic", nil)
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
