//go:build !linux

package agent

import (
	"context"
	"errors"
)

func ApplySwap(int) error {
	return errors.New("调整 SWAP 仅支持 Linux")
}

func ApplySwapContext(context.Context, int) error {
	return errors.New("调整 SWAP 仅支持 Linux")
}
