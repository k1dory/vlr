package daemon

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

func testPortalCfg() *config.Config {
	return &config.Config{
		Region:       "RU/Yandex",
		SubBaseURL:   "https://link.infrashark.tech",
		PortalHeader: "X-Authentik-Email",
		Entry: config.EntryConfig{
			Host: "node1.example.com", Port: 443, SNI: "www.example.com",
			PublicKey: "pk", ShortIDs: []string{"0a1b2c3d"}, Fingerprint: "randomized",
		},
		// Xray.ConfigPath empty => Apply is a no-op (no systemd needed in tests).
	}
}

func newPortalMux(t *testing.T, cfg *config.Config) (*http.ServeMux, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mux := http.NewServeMux()
	registerPortal(mux, cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return mux, st
}

// TestPortalRejectsNoIdentity: without the trusted email header, the portal must
// refuse — never render a page or create a user.
func TestPortalRejectsNoIdentity(t *testing.T) {
	mux, st := newPortalMux(t, testPortalCfg())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no header: status = %d, want 403", rec.Code)
	}
	if len(st.Users()) != 0 {
		t.Fatalf("no user must be created without identity, got %d", len(st.Users()))
	}
}

// TestPortalAutoProvisions: first authenticated visit creates the user keyed by
// email and renders a page containing that user's public subscription URL.
func TestPortalAutoProvisions(t *testing.T) {
	mux, st := newPortalMux(t, testPortalCfg())
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("X-Authentik-Email", "i.ivanov@genomed.ru")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	u, ok := st.FindUser("i.ivanov@genomed.ru")
	if !ok {
		t.Fatal("user was not auto-provisioned")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://link.infrashark.tech/base64/"+u.SubToken) {
		t.Fatalf("page missing personal sub URL; body:\n%s", body)
	}
	if !strings.Contains(body, "i.ivanov@genomed.ru") {
		t.Fatal("page should show the signed-in email")
	}

	// Second visit must reuse the same user (idempotent), not create a duplicate.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if n := len(st.Users()); n != 1 {
		t.Fatalf("second visit created a duplicate: %d users", n)
	}
}

// TestPortalDisabledWhenNoSubBaseURL: with SubBaseURL empty the portal is not
// mounted at all (route falls through to 404).
func TestPortalDisabledWhenNoSubBaseURL(t *testing.T) {
	cfg := testPortalCfg()
	cfg.SubBaseURL = ""
	mux, _ := newPortalMux(t, cfg)
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("X-Authentik-Email", "x@y.ru")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("portal must be unmounted; status = %d, want 404", rec.Code)
	}
}
