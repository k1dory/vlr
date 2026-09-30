package cascade

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitHealthyDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := WaitHealthy(ctx, func(context.Context) (bool, error) { return false, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected handshake timeout, got %v", err)
	}
}

func TestWaitHealthyCommandFailure(t *testing.T) {
	want := errors.New("awg missing")
	err := WaitHealthy(context.Background(), func(context.Context) (bool, error) { return false, want })
	if !errors.Is(err, want) {
		t.Fatalf("command error hidden: %v", err)
	}
}

func TestWaitHealthyRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	calls := 0
	err := WaitHealthy(ctx, func(context.Context) (bool, error) { calls++; return calls == 2, nil })
	if err != nil || calls != 2 {
		t.Fatalf("failed to wait for peer: %v (%d calls)", err, calls)
	}
}
