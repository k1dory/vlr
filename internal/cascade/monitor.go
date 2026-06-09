// Package cascade provides health monitoring for the RU->EU WireGuard hop and a
// place to hang the (future) Xray stats poller.
package cascade

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/k1dory/vlr/internal/store"
)

// WGMonitor reports the cascade healthy when the WireGuard peer has a recent
// handshake. It shells out to `wg show <iface> latest-handshakes` (Linux nodes).
type WGMonitor struct {
	Interface string
	// MaxAge is how stale the last handshake may be before unhealthy.
	MaxAge time.Duration
}

// Healthy returns true if the most recent handshake on the interface is within
// MaxAge. WireGuard rehandshakes roughly every 2 minutes when traffic flows.
func (m WGMonitor) Healthy(ctx context.Context) (bool, error) {
	maxAge := m.MaxAge
	if maxAge == 0 {
		maxAge = 3 * time.Minute
	}
	out, err := exec.CommandContext(ctx, "wg", "show", m.Interface, "latest-handshakes").Output()
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		ts, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || ts == 0 {
			continue
		}
		if now-ts <= int64(maxAge.Seconds()) {
			return true, nil
		}
	}
	return false, nil
}

// NoopMonitor always reports healthy. Used in dev / on Windows where there is no
// WireGuard interface to inspect.
type NoopMonitor struct{}

// Healthy always returns true.
func (NoopMonitor) Healthy(ctx context.Context) (bool, error) { return true, nil }

// NoopStats is a stats poller that does nothing. The real implementation will
// query Xray's StatsService (grpc on 127.0.0.1:10085) and call
// store.UpdateTraffic per user; that pulls in the Xray API proto, so it lives
// behind a build tag in a later iteration.
type NoopStats struct{}

// Poll does nothing.
func (NoopStats) Poll(ctx context.Context, s *store.Store) error { return nil }
