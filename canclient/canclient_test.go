package canclient

import "github.com/5uwifi/canchain"

var (
	_ = canchain.ChainReader(&Client{})
	_ = canchain.TransactionReader(&Client{})
	_ = canchain.ChainStateReader(&Client{})
	_ = canchain.ChainSyncReader(&Client{})
	_ = canchain.ContractCaller(&Client{})
	_ = canchain.GasEstimator(&Client{})
	_ = canchain.GasPricer(&Client{})
	_ = canchain.LogFilterer(&Client{})
	_ = canchain.PendingStateReader(&Client{})
	_ = canchain.PendingContractCaller(&Client{})
)
