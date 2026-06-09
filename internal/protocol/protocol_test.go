package protocol

import (
	"testing"
	"time"
)

func TestShouldPull(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	th := Thresholds{ByteDelta: 100 << 20, ReconcileEvery: 10 * time.Minute}

	tests := []struct {
		name     string
		view     NodeView
		wantPull bool
		wantWhy  string
	}{
		{
			name:     "never pulled but heard => baseline",
			view:     NodeView{LastSeen: now},
			wantPull: true,
			wantWhy:  "initial baseline pull",
		},
		{
			name: "config advanced => pull",
			view: NodeView{
				LastSeen: now, LastPull: now.Add(-time.Minute),
				PulledConfigVersion: 4, HeardConfigVersion: 5,
			},
			wantPull: true,
			wantWhy:  "config_version advanced",
		},
		{
			name: "traffic over threshold => pull",
			view: NodeView{
				LastSeen: now, LastPull: now.Add(-time.Minute),
				PulledConfigVersion: 5, HeardConfigVersion: 5,
				PulledTotalBytes: 0, HeardTotalBytes: 200 << 20,
			},
			wantPull: true,
			wantWhy:  "traffic delta over threshold",
		},
		{
			name: "small traffic, recent pull => no pull",
			view: NodeView{
				LastSeen: now, LastPull: now.Add(-time.Minute),
				PulledConfigVersion: 5, HeardConfigVersion: 5,
				PulledTotalBytes: 0, HeardTotalBytes: 1 << 20,
			},
			wantPull: false,
		},
		{
			name: "reconcile interval elapsed => pull",
			view: NodeView{
				LastSeen: now, LastPull: now.Add(-11 * time.Minute),
				PulledConfigVersion: 5, HeardConfigVersion: 5,
			},
			wantPull: true,
			wantWhy:  "reconcile interval elapsed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldPull(tc.view, now, th)
			if got.Pull != tc.wantPull {
				t.Fatalf("Pull = %v, want %v (reason %q)", got.Pull, tc.wantPull, got.Reason)
			}
			if tc.wantWhy != "" && got.Reason != tc.wantWhy {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantWhy)
			}
		})
	}
}

func TestMarkDown(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	interval := 20 * time.Second

	if MarkDown(time.Time{}, now, interval, 3) {
		t.Fatal("never-seen node must not be marked down")
	}
	if MarkDown(now.Add(-30*time.Second), now, interval, 3) {
		t.Fatal("30s < 3*20s, must not be down")
	}
	if !MarkDown(now.Add(-90*time.Second), now, interval, 3) {
		t.Fatal("90s > 3*20s, must be down")
	}
}
