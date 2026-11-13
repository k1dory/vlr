//go:build unix

package flock

import (
	"path/filepath"
	"testing"
	"time"
)

// Two locks on the same file from one process (separate descriptors) must
// exclude each other — xray.Apply and devlimit.ApplyBans rely on it.
func TestSameProcessContention(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u2, err := Lock(p)
		if err == nil {
			u2()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second lock acquired while the first is held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock not acquired after release")
	}
}
