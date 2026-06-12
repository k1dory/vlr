package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/protocol"
	"github.com/k1dory/vlr/internal/store"
)

// Child runs the same data plane as standalone, but reports to a main server:
// it PUSHES cheap heartbeats and EXPOSES a pull API the main calls on demand.
type Child struct {
	cfg   *config.Config
	store *store.Store
	log   *slog.Logger
	mon   CascadeMonitor
	stats StatsPoller
	http  *http.Client
	seq   atomic.Uint64
}

// NewChild wires a child daemon.
func NewChild(cfg *config.Config, st *store.Store, log *slog.Logger, stats StatsPoller, mon CascadeMonitor) *Child {
	return &Child{
		cfg: cfg, store: st, log: log, stats: stats, mon: mon,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Run starts the heartbeat loop and the pull API server, blocking until ctx is
// cancelled.
func (c *Child) Run(ctx context.Context) error {
	go c.heartbeatLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pull", c.authPull(c.handlePull))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		up, _ := c.mon.Healthy(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"cascade_up": up})
	})
	// Token-guarded user API (POST/DELETE /v1/users) — prod automation.
	registerUserAPI(mux, c.cfg, c.store, c.log)

	addr := c.cfg.Child.PullListen
	if addr == "" {
		addr = "127.0.0.1:9777"
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sd)
	}()

	c.log.Info("child up", "node", c.cfg.NodeID, "main", c.cfg.Child.MainURL, "pull_listen", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("pull server: %w", err)
	}
	return nil
}

func (c *Child) heartbeatLoop(ctx context.Context) {
	interval := time.Duration(c.cfg.Child.HeartbeatSeconds) * time.Second
	if interval <= 0 {
		interval = 20 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	c.sendHeartbeat(ctx) // immediate first beat
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.sendHeartbeat(ctx)
		}
	}
}

func (c *Child) sendHeartbeat(ctx context.Context) {
	up, _ := c.mon.Healthy(ctx)
	// Refresh per-user counters before reporting, so the heartbeat's total_bytes
	// is current. Without this, the main's traffic-delta pull trigger would only
	// see byte growth at pull time (chicken-and-egg) and fall back to the slow
	// reconcile timer. Best-effort: a stats failure must not drop the heartbeat.
	if c.stats != nil {
		if err := c.stats.Poll(ctx, c.store); err != nil {
			c.log.Warn("stats poll failed", "err", err)
		}
	}
	hb := protocol.Heartbeat{
		NodeID:        c.cfg.NodeID,
		Seq:           c.seq.Add(1),
		SentUnix:      time.Now().Unix(),
		Healthy:       up,
		CascadeUp:     up,
		UserCount:     len(c.store.Users()),
		ConfigVersion: c.store.ConfigVersion(),
		TotalBytes:    c.store.TotalBytes(),
	}
	body, _ := json.Marshal(hb)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.Child.MainURL+"/heartbeat", bytes.NewReader(body))
	if err != nil {
		c.log.Warn("heartbeat build failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.Child.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		// A failed heartbeat is itself the signal to main (it will see the gap).
		c.log.Warn("heartbeat push failed", "err", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		c.log.Warn("heartbeat rejected", "status", resp.StatusCode)
	}
}

// authPull guards the pull API with the bearer token the main must present.
func (c *Child) authPull(next http.HandlerFunc) http.HandlerFunc {
	want := "Bearer " + c.cfg.Child.PullBearer
	return func(w http.ResponseWriter, r *http.Request) {
		if c.cfg.Child.PullBearer == "" || r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handlePull returns the heavy per-user detail the main asks for.
func (c *Child) handlePull(w http.ResponseWriter, r *http.Request) {
	if c.stats != nil {
		_ = c.stats.Poll(r.Context(), c.store)
	}
	users := c.store.Users()
	det := make([]protocol.UserDetail, 0, len(users))
	for _, u := range users {
		det = append(det, protocol.UserDetail{
			UUID: u.UUID, Email: u.Email, TelegramID: u.TelegramID, ExternalID: u.ExternalID,
			Profile: u.Profile, ShortID: u.ShortID,
			Enabled: u.Enabled, RxBytes: u.RxBytes, TxBytes: u.TxBytes,
		})
	}
	writeJSON(w, http.StatusOK, protocol.PullResponse{
		NodeID:        c.cfg.NodeID,
		ConfigVersion: c.store.ConfigVersion(),
		Entry:         entrySnapshot(c.cfg.Entry),
		Users:         det,
	})
}

// entrySnapshot copies the PUBLIC Reality entry params for the main to rebuild
// links. The private key is deliberately omitted.
func entrySnapshot(e config.EntryConfig) protocol.EntrySnapshot {
	return protocol.EntrySnapshot{
		Host:        e.Host,
		Port:        e.Port,
		SNI:         e.SNI,
		PublicKey:   e.PublicKey,
		Fingerprint: e.Fingerprint,
		ShortIDs:    e.ShortIDs,
	}
}
