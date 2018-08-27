package kernel

import "errors"

var (
	ErrKnownBlock = errors.New("block already known")

	ErrGasLimitReached = errors.New("gas limit reached")

	ErrBlacklistedHash = errors.New("blacklisted hash")

	ErrNonceTooHigh = errors.New("nonce too high")
)
