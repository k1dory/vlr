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
	// GET /base64/<sub_token> -> base64 subscription for that user. This is the
	// public, unguessable URL fronted by link.infrashark.tech; the token carries
	// no email/UUID so it is safe in proxy logs and can be rotated to revoke.
	mux.HandleFunc("/base64/", func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Path[len("/base64/"):]
		if u, ok := s.store.FindBySubToken(tok); ok && u.Enabled {
			s.writeSubscription(w, u)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	// GET /sub/<email> -> same body, kept for backwards compatibility. Prefer
	// /base64/<token>: email in the path is enumerable and leaks into logs.
	mux.HandleFunc("/sub/", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Path[len("/sub/"):]
		for _, u := range s.store.Users() {
			if u.Email == email && u.Enabled {
				s.writeSubscription(w, u)
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
	// Event-driven auto-revoke: Authentik POSTs here on user deactivation/deletion.
	registerAuthentikWebhook(mux, s.cfg, s.store, s.log)
	// Self-service portal (GET /me), behind Authentik forward-auth. No-op unless
	// SubBaseURL is set. Registers "/" as the vhost root, so keep it last.
	registerPortal(mux, s.cfg, s.store, s.log)

	srv := &http.Server{Addr: subListen(s.cfg), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go s.monitorLoop(ctx)
	// Optional reconcile backstop (off unless a poll interval + API creds are set).
	// The webhook is the real-time path; this only catches missed events.
	if s.cfg.Authentik.Enabled && s.cfg.Authentik.ReconcileSeconds > 0 &&
		s.cfg.Authentik.APIURL != "" && s.cfg.Authentik.Token != "" {
		r := newAuthentikReconciler(s.cfg, s.store, s.log)
		go r.run(ctx)
		s.log.Info("authentik reconcile backstop enabled", "every_s", s.cfg.Authentik.ReconcileSeconds)
	}

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

// writeSubscription emits the base64 subscription for one user, plus the headers
// mainstream clients (v2rayNG/Hiddify/NekoBox) read: Profile-Title names the
// profile, Subscription-Userinfo shows this user's up/down counters.
func (s *Standalone) writeSubscription(w http.ResponseWriter, u store.User) {
	body := subscription.Stream(s.cfg.Entry, []store.User{u})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Profile-Title", "VLR "+s.cfg.Region)
	w.Header().Set("Subscription-Userinfo",
		fmt.Sprintf("upload=%d; download=%d", u.TxBytes, u.RxBytes))
	_, _ = w.Write([]byte(body))
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
