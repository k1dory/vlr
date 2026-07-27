package daemon

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
	"github.com/k1dory/vlr/internal/xray"
)

// authentikEvent is the (customized) webhook body Authentik sends on a user
// deactivation/deletion — shaped by the Notification Webhook Mapping documented
// in docs/AUTHENTIK.md. is_active is a pointer so an omitted field (a delete)
// reads as "not active" → revoke.
type authentikEvent struct {
	Action   string `json:"action"`    // "model_updated" | "model_deleted"
	Email    string `json:"email"`     // resolved by the Authentik body mapping
	IsActive *bool  `json:"is_active"` // false/absent on deactivation or delete
}

// registerAuthentikWebhook mounts POST /v1/authentik/event — the event-driven
// auto-revoke endpoint. Authentik calls it the instant a user is deactivated or
// deleted; the node removes that portal-provisioned user (Source==authentik) and
// reloads Xray, so access is pulled immediately without any polling.
//
// Guarded by Authorization: Bearer <cfg.Authentik.WebhookSecret>. No-op unless
// the SSO integration is enabled and a secret is set.
func registerAuthentikWebhook(mux *http.ServeMux, cfg *config.Config, st *store.Store, log *slog.Logger) {
	if !cfg.Authentik.Enabled || cfg.Authentik.WebhookSecret == "" {
		return
	}
	want := "Bearer " + cfg.Authentik.WebhookSecret
	mux.HandleFunc("/v1/authentik/event", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var ev authentikEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		email := strings.TrimSpace(ev.Email)
		// A reactivation/no-op: still active and not a delete → nothing to revoke.
		active := ev.IsActive != nil && *ev.IsActive
		if ev.Action != "model_deleted" && active {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": false, "reason": "still active"})
			return
		}
		if email == "" {
			// Well-formed but unactionable — 200 so Authentik doesn't retry-storm.
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": false, "reason": "no email"})
			return
		}
		revoked := revokeAuthentikUser(cfg, st, log, email)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": revoked})
	})
}

// revokeAuthentikUser removes the portal-provisioned user with this email and
// reloads Xray. It only touches Source==authentik users (never manual/API ones),
// and returns whether anything was removed. Idempotent: an unknown or
// already-removed user is a no-op.
func revokeAuthentikUser(cfg *config.Config, st *store.Store, log *slog.Logger, email string) bool {
	u, ok := st.FindUser(email)
	if !ok || u.Source != store.SourceAuthentik {
		return false
	}
	if err := st.RemoveUser(email); err != nil {
		log.Warn("authentik webhook: remove failed", "email", email, "err", err)
		return false
	}
	if err := xray.Apply(cfg, st.Users()); err != nil {
		log.Warn("authentik webhook: xray apply failed", "err", err)
	}
	log.Info("authentik webhook: revoked user", "email", email)
	return true
}
