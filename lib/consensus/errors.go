package consensus

import "errors"

var (
	ErrUnknownAncestor = errors.New("unknown ancestor")

	ErrPrunedAncestor = errors.New("pruned ancestor")

	ErrFutureBlock = errors.New("block in the future")

	ErrInvalidNumber = errors.New("invalid block number")
)
