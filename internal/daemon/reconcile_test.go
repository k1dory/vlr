package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

// authentikStub serves a minimal /api/v3/core/users/ that returns the given
// active emails as a single page. status != 0 overrides the response code.
func authentikStub(t *testing.T, emails []string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		results := make([]map[string]any, 0, len(emails))
		for _, e := range emails {
			results = append(results, map[string]any{"email": e, "is_active": true})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pagination": map[string]any{"next": 0, "current": 1},
			"results":    results,
		})
	}))
}

func reconcilerFor(t *testing.T, srvURL string) (*authentikReconciler, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := &config.Config{Authentik: config.AuthentikConfig{
		Enabled: true, APIURL: srvURL, Token: "test-token",
	}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newAuthentikReconciler(cfg, st, log), st
}

func TestReconcileRevokesDeactivated(t *testing.T) {
	srv := authentikStub(t, []string{"active@genomed.ru"}, 0)
	defer srv.Close()
	r, st := reconcilerFor(t, srv.URL)

	// Portal-provisioned users: one still active upstream, one deactivated.
	mustAdd(t, st, store.User{UUID: "u1", Email: "active@genomed.ru", Source: store.SourceAuthentik})
	mustAdd(t, st, store.User{UUID: "u2", Email: "gone@genomed.ru", Source: store.SourceAuthentik})
	// A manually-created (non-Authentik) user must never be auto-removed.
	mustAdd(t, st, store.User{UUID: "u3", TelegramID: 42})

	r.reconcileOnce(context.Background())

	if _, ok := st.FindUser("active@genomed.ru"); !ok {
		t.Error("active Authentik user was wrongly removed")
	}
	if _, ok := st.FindUser("gone@genomed.ru"); ok {
		t.Error("deactivated Authentik user should have been revoked")
	}
	if _, ok := st.FindUser("42"); !ok {
		t.Error("manual (non-Authentik) user must never be auto-removed")
	}
}

func TestReconcileFailSafeOnEmpty(t *testing.T) {
	srv := authentikStub(t, nil, 0) // API returns zero active users
	defer srv.Close()
	r, st := reconcilerFor(t, srv.URL)
	mustAdd(t, st, store.User{UUID: "u1", Email: "someone@genomed.ru", Source: store.SourceAuthentik})

	r.reconcileOnce(context.Background())

	if _, ok := st.FindUser("someone@genomed.ru"); !ok {
		t.Error("empty active set must NOT prune (fail-safe)")
	}
}

func TestReconcileFailSafeOnError(t *testing.T) {
	srv := authentikStub(t, nil, http.StatusInternalServerError)
	defer srv.Close()
	r, st := reconcilerFor(t, srv.URL)
	mustAdd(t, st, store.User{UUID: "u1", Email: "someone@genomed.ru", Source: store.SourceAuthentik})

	r.reconcileOnce(context.Background())

	if _, ok := st.FindUser("someone@genomed.ru"); !ok {
		t.Error("API error must NOT prune (fail-safe)")
	}
}

func mustAdd(t *testing.T, st *store.Store, u store.User) {
	t.Helper()
	if err := st.AddUser(u); err != nil {
		t.Fatalf("add %q: %v", u.Email, err)
	}
}
