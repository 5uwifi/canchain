package downloader

import (
	"github.com/5uwifi/canchain/lib/metrics"
)

var (
	headerInMeter      = metrics.NewRegisteredMeter("can/downloader/headers/in", nil)
	headerReqTimer     = metrics.NewRegisteredTimer("can/downloader/headers/req", nil)
	headerDropMeter    = metrics.NewRegisteredMeter("can/downloader/headers/drop", nil)
	headerTimeoutMeter = metrics.NewRegisteredMeter("can/downloader/headers/timeout", nil)

	bodyInMeter      = metrics.NewRegisteredMeter("can/downloader/bodies/in", nil)
	bodyReqTimer     = metrics.NewRegisteredTimer("can/downloader/bodies/req", nil)
	bodyDropMeter    = metrics.NewRegisteredMeter("can/downloader/bodies/drop", nil)
	bodyTimeoutMeter = metrics.NewRegisteredMeter("can/downloader/bodies/timeout", nil)

	receiptInMeter      = metrics.NewRegisteredMeter("can/downloader/receipts/in", nil)
	receiptReqTimer     = metrics.NewRegisteredTimer("can/downloader/receipts/req", nil)
	receiptDropMeter    = metrics.NewRegisteredMeter("can/downloader/receipts/drop", nil)
	receiptTimeoutMeter = metrics.NewRegisteredMeter("can/downloader/receipts/timeout", nil)

	stateInMeter   = metrics.NewRegisteredMeter("can/downloader/states/in", nil)
	stateDropMeter = metrics.NewRegisteredMeter("can/downloader/states/drop", nil)
)
