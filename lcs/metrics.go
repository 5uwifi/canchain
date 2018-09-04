package lcs

import (
	"github.com/5uwifi/canchain/lib/metrics"
	"github.com/5uwifi/canchain/lib/p2p"
)

var (
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
