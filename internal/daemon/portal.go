package daemon

import (
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/qr"
	"github.com/k1dory/vlr/internal/store"
	"github.com/k1dory/vlr/internal/subscription"
	"github.com/k1dory/vlr/internal/util"
	"github.com/k1dory/vlr/internal/xray"
)

// registerPortal mounts the self-service subscription portal at GET /me (and the
// vhost root /). It is meant to sit behind an Authentik forward-auth proxy that
// injects the authenticated user's email in cfg.PortalHeader (default
// X-Authentik-Email). The handler:
//
//  1. reads the trusted email header (a browser cannot set it — the fronting
//     proxy overwrites it),
//  2. finds that user, AUTO-PROVISIONING one on first visit (create + apply Xray),
//  3. renders a personal page with the ready-to-import subscription URL.
//
// It is enabled only when cfg.SubBaseURL is set (the portal exists to hand out
// that public URL). Bind the node to loopback/management and let only the
// Authentik Caddy vhost reach it — see deploy/caddy/sub.genomed-security.ru.
func registerPortal(mux *http.ServeMux, cfg *config.Config, st *store.Store, log *slog.Logger) {
	if cfg.SubBaseURL == "" {
		return
	}
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		email := strings.TrimSpace(r.Header.Get(cfg.PortalHeader))
		// A valid identity is required. No header => the request did not come
		// through the authenticating proxy; refuse rather than leak a page.
		if !looksLikeEmail(email) {
			http.Error(w, "forbidden: no authenticated identity", http.StatusForbidden)
			return
		}
		u, ok := st.FindUser(email)
		if !ok {
			created, err := provisionPortalUser(cfg, st, email, log)
			if err != nil {
				log.Warn("portal provision failed", "email", email, "err", err)
				http.Error(w, "provisioning failed", http.StatusInternalServerError)
				return
			}
			u = created
			log.Info("portal auto-provisioned user", "email", email, "uuid", u.UUID)
		}
		if !u.Enabled {
			http.Error(w, "account disabled", http.StatusForbidden)
			return
		}
		subURL := subscription.PublicURL(cfg.SubBaseURL, u)
		renderPortal(w, portalData{
			Email:  email,
			Region: cfg.Region,
			SubURL: subURL,
			QR:     qrSVG(subURL),
			Link:   subscription.Link(cfg.Entry, u),
		}, log)
	}
	mux.HandleFunc("/me", h)
	mux.HandleFunc("/", h) // vhost root (sub.genomed-security.ru/)
}

// looksLikeEmail is a cheap sanity check so an empty/garbage header never creates
// a junk user. Full RFC validation is unnecessary — the identity is authoritative
// once the proxy set it; we only guard against blank/non-address values.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n")
}

// provisionPortalUser creates a user keyed by email and applies the new Xray
// config so the credential is live immediately, then returns the stored copy.
func provisionPortalUser(cfg *config.Config, st *store.Store, email string, log *slog.Logger) (store.User, error) {
	uuid, err := util.NewUUID()
	if err != nil {
		return store.User{}, err
	}
	sid := ""
	if len(cfg.Entry.ShortIDs) > 0 {
		sid = cfg.Entry.ShortIDs[len(st.Users())%len(cfg.Entry.ShortIDs)]
	}
	if err := st.AddUser(store.User{UUID: uuid, Email: email, ShortID: sid, Source: store.SourceAuthentik}); err != nil {
		// A racing request may have created it first — fall through to the lookup.
		if existing, ok := st.FindUser(email); ok {
			return existing, nil
		}
		return store.User{}, err
	}
	if err := xray.Apply(cfg, st.Users()); err != nil {
		log.Warn("xray auto-apply failed", "err", err)
	}
	stored, _ := st.FindUser(email)
	return stored, nil
}

type portalData struct {
	Email  string
	Region string
	SubURL string
	QR     template.HTML // inline <svg> of the subscription URL (empty if it won't fit)
	Link   string
}

// qrSVG renders url as an inline QR <svg>, or "" if the URL is empty or too long
// to encode (the page still shows the link text either way).
func qrSVG(url string) template.HTML {
	if url == "" {
		return ""
	}
	code, err := qr.Encode(url)
	if err != nil {
		return ""
	}
	return template.HTML(code.SVG(4, 4)) // 4-module quiet zone, 4px per module
}

func renderPortal(w http.ResponseWriter, d portalData, log *slog.Logger) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := portalTmpl.Execute(w, d); err != nil {
		log.Warn("portal render failed", "err", err)
	}
}

// portalTmpl is the personal page. html/template auto-escapes every field, so the
// email/URLs are safe to interpolate. The Hiddify deep-link imports the sub in one
// tap on the same phone; desktop users copy the URL. QR is intentionally omitted
// (adding it correctly needs a vendored JS encoder) — the copy + deep-link cover
// both cases.
var portalTmpl = template.Must(template.New("portal").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Личный VPN</title>
<style>
:root{--bg:#f6f7f9;--card:#fff;--fg:#1c2430;--muted:#5b6673;--line:#e4e8ee;--accent:#2f6bff;--code-bg:#0f1622;--code-fg:#e6edf3;--ok:#1a8a52}
@media(prefers-color-scheme:dark){:root{--bg:#0f1420;--card:#161d2b;--fg:#e7ecf3;--muted:#9aa7b8;--line:#26304260;--accent:#5b8cff;--code-bg:#0b111b}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
.wrap{max-width:640px;margin:0 auto;padding:32px 20px 80px}h1{font-size:1.6rem;margin:.2em 0}
.lead{color:var(--muted);margin:0 0 24px}.card{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:22px;margin:16px 0}
.who{color:var(--muted);font-size:.9rem;margin-bottom:18px}.url{display:block;background:var(--code-bg);color:var(--code-fg);padding:14px;border-radius:8px;font-family:ui-monospace,Menlo,monospace;font-size:.85rem;word-break:break-all;margin:8px 0}
.btns{display:flex;flex-wrap:wrap;gap:10px;margin-top:14px}button,a.btn{cursor:pointer;border:0;border-radius:10px;padding:12px 18px;font-size:1rem;font-weight:600;text-decoration:none;display:inline-block}
.primary{background:var(--accent);color:#fff}.ghost{background:transparent;border:1px solid var(--line);color:var(--fg)}
ol{padding-left:1.2em}li{margin:.35em 0}.ok{color:var(--ok)}.muted{color:var(--muted);font-size:.9rem}
.qr{display:flex;justify-content:center;margin:6px 0 18px}.qr svg{width:220px;height:220px;border:8px solid #fff;border-radius:10px}
</style></head><body><div class="wrap">
<h1>Личный VPN {{if .Region}}· {{.Region}}{{end}}</h1>
<p class="lead">Ваша персональная подписка. Импортируется в приложение один раз — серверы обновляются сами.</p>
<div class="card">
  <div class="who">Вы вошли как <b>{{.Email}}</b></div>
  {{if .QR}}<div class="qr">{{.QR}}</div>
  <p class="muted" style="text-align:center;margin-top:-10px">Наведите камеру приложения на QR</p>{{end}}
  <label class="muted">…или ссылка-подписка:</label>
  <span class="url" id="sub">{{.SubURL}}</span>
  <div class="btns">
    <button class="primary" onclick="copySub()">Копировать ссылку</button>
    <a class="btn ghost" href="hiddify://import/{{.SubURL}}">Открыть в Hiddify</a>
  </div>
  <p class="muted" id="copied" style="visibility:hidden">Скопировано ✓</p>
</div>
<div class="card">
  <b>Как подключить</b>
  <ol>
    <li>Поставьте <b>Hiddify</b> (Android/iOS/Windows/macOS) или v2rayNG/NekoBox.</li>
    <li>На телефоне нажмите «Открыть в Hiddify» выше — подписка импортируется сама.</li>
    <li>На компьютере — «Копировать ссылку», затем в приложении «Импорт из буфера».</li>
    <li>Выберите профиль <b>VLR</b> и нажмите подключение.</li>
  </ol>
  <p class="muted">Профиль по умолчанию работает везде; <code>vision</code> — только мобильные.</p>
</div>
<p class="muted">Ссылка личная — не пересылайте. Утекла? Сообщите админу, он выдаст новую (старая сразу отключится).</p>
<script>
function copySub(){var t=document.getElementById('sub').textContent.trim();
navigator.clipboard.writeText(t).then(function(){var c=document.getElementById('copied');c.style.visibility='visible';setTimeout(function(){c.style.visibility='hidden'},2000)})}
</script>
</div></body></html>`))
