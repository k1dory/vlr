package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
	"github.com/k1dory/vlr/internal/xray"
)

// authentikReconciler is the OPTIONAL backstop for the event-driven webhook
// (webhook.go): if a deactivation webhook is ever missed (node down, dropped
// request), a low-frequency reconcile catches the drift. It fetches the set of
// ACTIVE users from Authentik and removes every portal-provisioned user
// (Source==authentik) whose email is no longer active.
//
// This is NOT the primary path and is off unless ReconcileSeconds>0. Run it
// rarely (hours/day) — the webhook does the real-time work.
//
// Fail-safe by design: if the Authentik fetch errors OR returns an empty active
// set, NOTHING is pruned. A transient API outage must never mass-delete users.
type authentikReconciler struct {
	cfg   *config.Config
	store *store.Store
	log   *slog.Logger
	http  *http.Client
}

func newAuthentikReconciler(cfg *config.Config, st *store.Store, log *slog.Logger) *authentikReconciler {
	return &authentikReconciler{
		cfg: cfg, store: st, log: log,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// run loops until ctx is cancelled, reconciling on the configured interval.
func (a *authentikReconciler) run(ctx context.Context) {
	interval := time.Duration(a.cfg.Authentik.ReconcileSeconds) * time.Second
	if interval <= 0 {
		interval = 300 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// Reconcile once shortly after start, then on the ticker.
	a.reconcileOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reconcileOnce(ctx)
		}
	}
}

func (a *authentikReconciler) reconcileOnce(ctx context.Context) {
	active, err := a.fetchActiveEmails(ctx)
	if err != nil {
		a.log.Warn("authentik reconcile: fetch failed, skipping prune", "err", err)
		return
	}
	if len(active) == 0 {
		a.log.Warn("authentik reconcile: active set empty, skipping prune (fail-safe)")
		return
	}
	removed := a.prune(active)
	if removed > 0 {
		if err := xray.Apply(a.cfg, a.store.Users()); err != nil {
			a.log.Warn("authentik reconcile: xray apply failed", "err", err)
		}
		a.log.Info("authentik reconcile: revoked deactivated users", "removed", removed, "active", len(active))
	}
}

// prune removes portal-provisioned users whose email is not in the active set.
// Returns the number removed. Comparison is case-insensitive.
func (a *authentikReconciler) prune(active map[string]bool) int {
	removed := 0
	for _, u := range a.store.Users() {
		if u.Source != store.SourceAuthentik || u.Email == "" {
			continue // only auto-manage what the portal created
		}
		if active[strings.ToLower(u.Email)] {
			continue // still active upstream
		}
		if err := a.store.RemoveUser(u.Email); err != nil {
			a.log.Warn("authentik reconcile: remove failed", "email", u.Email, "err", err)
			continue
		}
		a.log.Info("authentik reconcile: revoked user", "email", u.Email)
		removed++
	}
	return removed
}

// authentikUser is the slice of Authentik's user object we care about.
type authentikUser struct {
	Email    string `json:"email"`
	IsActive bool   `json:"is_active"`
}

type authentikPage struct {
	Pagination struct {
		Next    int `json:"next"`
		Current int `json:"current"`
	} `json:"pagination"`
	Results []authentikUser `json:"results"`
}

// fetchActiveEmails pulls every active user from Authentik (paginated) and returns
// their lowercased emails as a set.
func (a *authentikReconciler) fetchActiveEmails(ctx context.Context) (map[string]bool, error) {
	base := strings.TrimRight(a.cfg.Authentik.APIURL, "/")
	if base == "" || a.cfg.Authentik.Token == "" {
		return nil, fmt.Errorf("authentik api_url/token not configured")
	}
	out := map[string]bool{}
	page := 1
	for {
		u := fmt.Sprintf("%s/api/v3/core/users/?is_active=true&page_size=100&page=%d", base, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+a.cfg.Authentik.Token)
		req.Header.Set("Accept", "application/json")
		resp, err := a.http.Do(req)
		if err != nil {
			return nil, err
		}
		var pg authentikPage
		dec := json.NewDecoder(resp.Body)
		derr := dec.Decode(&pg)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("authentik users API: status %d", resp.StatusCode)
		}
		if derr != nil {
			return nil, fmt.Errorf("decode authentik page: %w", derr)
		}
		for _, ru := range pg.Results {
			if ru.IsActive && ru.Email != "" {
				out[strings.ToLower(ru.Email)] = true
			}
		}
		// Authentik sets pagination.next to the next page number, or 0 when done.
		if pg.Pagination.Next == 0 || pg.Pagination.Next <= pg.Pagination.Current {
			break
		}
		page = pg.Pagination.Next
	}
	return out, nil
}
