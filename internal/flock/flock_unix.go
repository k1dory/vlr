//go:build unix

// Package flock provides an exclusive advisory lock on a file, used to serialize
// read-modify-write cycles between the vlr CLI and the running daemon.
package flock

import (
	"os"
	"syscall"
)

// Lock takes an exclusive flock on path (created if missing) and returns the
// unlock function.
func Lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
