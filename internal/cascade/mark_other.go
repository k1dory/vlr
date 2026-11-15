//go:build !linux

package cascade

import (
	"errors"
	"syscall"
)

func markControl(int) (func(network, address string, c syscall.RawConn) error, error) {
	return nil, errors.New("SO_MARK is only supported on Linux")
}
