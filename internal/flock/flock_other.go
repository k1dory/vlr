//go:build !unix

// Package flock provides an exclusive advisory lock on a file. Off unix (dev
// builds) it is a no-op; callers still hold their in-process mutex.
package flock

// Lock is a no-op off unix.
func Lock(string) (func(), error) { return func() {}, nil }
