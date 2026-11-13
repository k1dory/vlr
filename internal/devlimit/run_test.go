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
	mu         sync.Mutex
	failFirst  int
	pushes     [][]Ban
	online     map[string][]string
	onlineErrs []bool // per Online call: true = fail (consumed in order)
	failPushes []bool // per ApplyBans call after failFirst: true = fail
}

func (f *fakeApplier) Online(context.Context) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.onlineErrs) > 0 {
		fail := f.onlineErrs[0]
		f.onlineErrs = f.onlineErrs[1:]
		if fail {
			return nil, errors.New("api down")
		}
	}
	return f.online, nil
}

func (f *fakeApplier) ApplyBans(_ context.Context, b []Ban) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFirst > 0 {
		f.failFirst--
		return errors.New("api down")
	}
	if len(f.failPushes) > 0 {
		fail := f.failPushes[0]
		f.failPushes = f.failPushes[1:]
		if fail {
			return errors.New("push failed")
		}
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

func TestRunEnforcesBan(t *testing.T) {
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

// Review pass 3, finding 2: bans active, a push fails, then observing online IPs
// fails: the retry must push the last computed bans, not an empty set.
func TestRunRetryKeepsBansWhenObservationFails(t *testing.T) {
	f := &fakeApplier{
		online:     map[string][]string{"u": {"1.1.1.1", "2.2.2.2"}},
		onlineErrs: []bool{false, true}, // tick 1 ok (ban computed), tick 2 Online fails
		failPushes: []bool{true, false}, // tick 1 push fails, tick 2 retry succeeds
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, quiet(), Policy{Max: 1, Ban: time.Minute}, f, time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.pushes)
		var first []Ban
		if n > 0 {
			first = f.pushes[0]
		}
		f.mu.Unlock()
		if n > 0 {
			if len(first) != 1 || first[0].IP != "2.2.2.2" {
				t.Fatalf("retry after failed observation pushed %v, want the active ban", first)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no successful push")
}
