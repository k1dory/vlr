// Package daemon implements the three vlr run modes: standalone, child, main.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
	"github.com/k1dory/vlr/internal/subscription"
)

// Standalone runs a self-contained node: it owns its users, monitors itself and
// serves its own base64 subscription. Nothing leaves the box.
type Standalone struct {
	cfg   *config.Config
	store *store.Store
	log   *slog.Logger
	stats StatsPoller
	mon   CascadeMonitor
}

// StatsPoller fills per-user counters from the Xray stats API. A nil/Noop poller
// is fine in dev; the real one talks to 127.0.0.1:10085.
type StatsPoller interface {
	Poll(ctx context.Context, s *store.Store) error
}

// CascadeMonitor reports whether the RU->EU WireGuard hop is healthy.
type CascadeMonitor interface {
	Healthy(ctx context.Context) (bool, error)
}

// NewStandalone wires a standalone daemon.
func NewStandalone(cfg *config.Config, st *store.Store, log *slog.Logger, stats StatsPoller, mon CascadeMonitor) *Standalone {
	return &Standalone{cfg: cfg, store: st, log: log, stats: stats, mon: mon}
}

// Run blocks until ctx is cancelled, serving the subscription endpoint and
// running the local monitor loop.
func (s *Standalone) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	// GET /sub/<email> -> base64 subscription for that user.
	mux.HandleFunc("/sub/", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Path[len("/sub/"):]
		for _, u := range s.store.Users() {
			if u.Email == email && u.Enabled {
				body := subscription.Stream(s.cfg.Entry, []store.User{u})
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Profile-Title", "VLR "+s.cfg.Region)
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		up, _ := s.mon.Healthy(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"node":       s.cfg.NodeID,
			"cascade_up": up,
			"users":      len(s.store.Users()),
		})
	})
	// Token-guarded user API (POST/DELETE /v1/users) — prod automation.
	registerUserAPI(mux, s.cfg, s.store, s.log)

	srv := &http.Server{Addr: subListen(s.cfg), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go s.monitorLoop(ctx)

	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sd)
	}()

	s.log.Info("standalone up", "node", s.cfg.NodeID, "sub_listen", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("subscription server: %w", err)
	}
	return nil
}

func (s *Standalone) monitorLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.stats != nil {
				if err := s.stats.Poll(ctx, s.store); err != nil {
					s.log.Warn("stats poll failed", "err", err)
				}
			}
			up, err := s.mon.Healthy(ctx)
			if err != nil || !up {
				s.log.Warn("cascade unhealthy", "up", up, "err", err)
			}
		}
	}
}

// subListen returns the bind address for the subscription server.
func subListen(c *config.Config) string {
	if c.Child.PullListen != "" {
		return c.Child.PullListen
	}
	return "127.0.0.1:9777"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
