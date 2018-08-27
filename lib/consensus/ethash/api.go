package ethash

import (
	"errors"

	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/hexutil"
	"github.com/5uwifi/canchain/kernel/types"
)

var errEthashStopped = errors.New("ethash stopped")

type API struct {
	ethash *Ethash
}

func (api *API) GetWork() ([3]string, error) {
	if api.ethash.config.PowMode != ModeNormal && api.ethash.config.PowMode != ModeTest {
		return [3]string{}, errors.New("not supported")
	}

	var (
		workCh = make(chan [3]string, 1)
		errc   = make(chan error, 1)
	)

	select {
	case api.ethash.fetchWorkCh <- &sealWork{errc: errc, res: workCh}:
	case <-api.ethash.exitCh:
		return [3]string{}, errEthashStopped
	}

	select {
	case work := <-workCh:
		return work, nil
	case err := <-errc:
		return [3]string{}, err
	}
}

func (api *API) SubmitWork(nonce types.BlockNonce, hash, digest common.Hash) bool {
	if api.ethash.config.PowMode != ModeNormal && api.ethash.config.PowMode != ModeTest {
		return false
	}

	var errc = make(chan error, 1)

	select {
	case api.ethash.submitWorkCh <- &mineResult{
		nonce:     nonce,
		mixDigest: digest,
		hash:      hash,
		errc:      errc,
	}:
	case <-api.ethash.exitCh:
		return false
	}

	err := <-errc
	return err == nil
}

func (api *API) SubmitHashRate(rate hexutil.Uint64, id common.Hash) bool {
	if api.ethash.config.PowMode != ModeNormal && api.ethash.config.PowMode != ModeTest {
		return false
	}

	var done = make(chan struct{}, 1)

	select {
	case api.ethash.submitRateCh <- &hashrate{done: done, rate: uint64(rate), id: id}:
	case <-api.ethash.exitCh:
		return false
	}

	<-done

	return true
}

func (api *API) GetHashrate() uint64 {
	return uint64(api.ethash.Hashrate())
}
