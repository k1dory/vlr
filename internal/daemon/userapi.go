package daemon

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
	"github.com/k1dory/vlr/internal/subscription"
	"github.com/k1dory/vlr/internal/util"
	"github.com/k1dory/vlr/internal/xray"
)

// createUserReq is the body of POST /v1/users. Every field is OPTIONAL — prod
// automation can send just {"telegram_id": 9876567} or even {}.
type createUserReq struct {
	TelegramID int64  `json:"telegram_id"`
	ID         string `json:"id"`      // external/system id
	Email      string `json:"email"`   // optional label
	Profile    string `json:"profile"` // mobile|desktop, default mobile
}

type createUserResp struct {
	UUID         string `json:"uuid"`
	Link         string `json:"link"`
	Subscription string `json:"subscription"` // base64
}

// registerUserAPI mounts the token-guarded user endpoints on mux:
//
//	POST   /v1/users        create a user, auto-apply Xray, return link+sub
//	DELETE /v1/users/<ref>  delete by uuid|email|id|telegram-id, auto-apply
//	GET    /v1/users        list (no secrets)
//
// Disabled (401) when cfg.APIToken is empty.
func registerUserAPI(mux *http.ServeMux, cfg *config.Config, st *store.Store, log *slog.Logger) {
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		want := "Bearer " + cfg.APIToken
		return func(w http.ResponseWriter, r *http.Request) {
			if cfg.APIToken == "" || r.Header.Get("Authorization") != want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	mux.HandleFunc("/v1/users", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			createUser(w, r, cfg, st, log)
		case http.MethodGet:
			writeJSON(w, http.StatusOK, st.Users())
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/v1/users/", auth(func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimPrefix(r.URL.Path, "/v1/users/")
		if r.Method != http.MethodDelete || ref == "" {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if err := st.RemoveUser(ref); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		applyAndLog(cfg, st, log)
		writeJSON(w, http.StatusOK, map[string]any{"removed": ref})
	}))
}

func createUser(w http.ResponseWriter, r *http.Request, cfg *config.Config, st *store.Store, log *slog.Logger) {
	var req createUserReq
	if body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16)); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
	}
	uuid, err := util.NewUUID()
	if err != nil {
		http.Error(w, "uuid", http.StatusInternalServerError)
		return
	}
	profile := req.Profile // "" = plain Reality (all devices); "vision" = mobile-only
	sid := ""
	if len(cfg.Entry.ShortIDs) > 0 {
		sid = cfg.Entry.ShortIDs[len(st.Users())%len(cfg.Entry.ShortIDs)]
	}
	u := store.User{
		UUID: uuid, Email: req.Email, ExternalID: req.ID, TelegramID: req.TelegramID,
		ShortID: sid, Profile: profile,
	}
	if err := st.AddUser(u); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Re-fetch so we have the stored copy (Enabled=true) for the subscription.
	if stored, ok := st.FindUser(u.UUID); ok {
		u = stored
	}
	applyAndLog(cfg, st, log)
	writeJSON(w, http.StatusCreated, createUserResp{
		UUID:         u.UUID,
		Link:         subscription.Link(cfg.Entry, u),
		Subscription: subscription.Stream(cfg.Entry, []store.User{u}),
	})
}

func applyAndLog(cfg *config.Config, st *store.Store, log *slog.Logger) {
	if err := xray.Apply(cfg, st.Users()); err != nil {
		log.Warn("xray auto-apply failed", "err", err)
	}
}
