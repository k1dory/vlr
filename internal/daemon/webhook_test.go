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

func webhookMux(t *testing.T) (*http.ServeMux, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := &config.Config{Authentik: config.AuthentikConfig{Enabled: true, WebhookSecret: "s3cr3t"}}
	mux := http.NewServeMux()
	registerAuthentikWebhook(mux, cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return mux, st
}

func post(mux *http.ServeMux, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/authentik/event", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestWebhookRejectsBadSecret(t *testing.T) {
	mux, st := webhookMux(t)
	mustAdd(t, st, store.User{UUID: "u1", Email: "a@x.ru", Source: store.SourceAuthentik})
	rec := post(mux, "Bearer wrong", `{"action":"model_deleted","email":"a@x.ru"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if _, ok := st.FindUser("a@x.ru"); !ok {
		t.Fatal("user must not be removed on bad auth")
	}
}

func TestWebhookRevokesOnDeactivation(t *testing.T) {
	mux, st := webhookMux(t)
	mustAdd(t, st, store.User{UUID: "u1", Email: "a@x.ru", Source: store.SourceAuthentik})
	rec := post(mux, "Bearer s3cr3t", `{"action":"model_updated","email":"a@x.ru","is_active":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, ok := st.FindUser("a@x.ru"); ok {
		t.Fatal("deactivated user should have been revoked")
	}
}

func TestWebhookRevokesOnDelete(t *testing.T) {
	mux, st := webhookMux(t)
	mustAdd(t, st, store.User{UUID: "u1", Email: "a@x.ru", Source: store.SourceAuthentik})
	rec := post(mux, "Bearer s3cr3t", `{"action":"model_deleted","email":"a@x.ru"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, ok := st.FindUser("a@x.ru"); ok {
		t.Fatal("deleted user should have been revoked")
	}
}

func TestWebhookNoopOnStillActive(t *testing.T) {
	mux, st := webhookMux(t)
	mustAdd(t, st, store.User{UUID: "u1", Email: "a@x.ru", Source: store.SourceAuthentik})
	rec := post(mux, "Bearer s3cr3t", `{"action":"model_updated","email":"a@x.ru","is_active":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, ok := st.FindUser("a@x.ru"); !ok {
		t.Fatal("still-active user must NOT be revoked")
	}
}

func TestWebhookLeavesManualUsers(t *testing.T) {
	mux, st := webhookMux(t)
	// A manually-created user (no Source) with the same email must never be nuked
	// by a webhook — only Source==authentik users are auto-managed.
	mustAdd(t, st, store.User{UUID: "u1", Email: "a@x.ru"})
	rec := post(mux, "Bearer s3cr3t", `{"action":"model_deleted","email":"a@x.ru"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, ok := st.FindUser("a@x.ru"); !ok {
		t.Fatal("manual (non-Authentik) user must be left untouched")
	}
}

func TestWebhookUnknownUser200(t *testing.T) {
	mux, _ := webhookMux(t)
	// Unknown user: still 200 (so Authentik does not retry-storm), revoked=false.
	rec := post(mux, "Bearer s3cr3t", `{"action":"model_deleted","email":"ghost@x.ru"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestWebhookDisabledWithoutSecret(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	cfg := &config.Config{Authentik: config.AuthentikConfig{Enabled: true}} // no secret
	mux := http.NewServeMux()
	registerAuthentikWebhook(mux, cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := post(mux, "Bearer s3cr3t", `{"action":"model_deleted","email":"a@x.ru"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("endpoint must be unmounted without a secret; status = %d, want 404", rec.Code)
	}
}
