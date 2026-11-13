package devlimit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeApplier struct {
	mu        sync.Mutex
	failFirst int
	pushes    [][]Ban
	online    map[string][]string
}

func (f *fakeApplier) Online(context.Context) (map[string][]string, error) { return f.online, nil }

func (f *fakeApplier) ApplyBans(_ context.Context, b []Ban) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFirst > 0 {
		f.failFirst--
		return errors.New("api down")
	}
	f.pushes = append(f.pushes, append([]Ban(nil), b...))
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Re-review finding 2: with the limit off, a failed startup cleanup is retried
// until it succeeds, then the worker exits.
func TestRunClearsStaleBansWithRetryWhenLimitOff(t *testing.T) {
	f := &fakeApplier{failFirst: 2}
	done := make(chan struct{})
	go func() { Run(context.Background(), quiet(), Policy{Max: 0}, f, time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish after cleanup succeeded")
	}
	if len(f.pushes) != 1 || len(f.pushes[0]) != 0 {
		t.Fatalf("want one successful empty push, got %v", f.pushes)
	}
}

func TestRunFirstPushIsEmptyThenEnforces(t *testing.T) {
	f := &fakeApplier{online: map[string][]string{"u": {"1.1.1.1", "2.2.2.2"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, quiet(), Policy{Max: 1, Ban: time.Minute}, f, time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.pushes)
		var last []Ban
		if n > 0 {
			last = f.pushes[n-1]
		}
		f.mu.Unlock()
		if n > 0 && len(last) == 1 && last[0].IP == "2.2.2.2" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ban never pushed: %v", f.pushes)
}
