// Package subscription builds VLESS+Reality share links and the base64
// subscription stream that v2rayNG / Hiddify / NekoBox import.
//
// A "subscription" is just newline-joined vless:// URLs, base64-encoded. That is
// what the user asked for: stream the node's access config into a single base64
// link. Standalone nodes serve it themselves; the main server serves it centrally.
package subscription

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

// Link renders one vless:// share URL for a user against an entry config.
//
// Resulting shape:
//
//	vless://<uuid>@<host>:<port>?type=tcp&security=reality&sni=<sni>
//	  &fp=<fingerprint>&pbk=<publicKey>&sid=<shortID>&flow=<flow>&spx=%2F#<label>
func Link(entry config.EntryConfig, u store.User) string {
	q := url.Values{}
	q.Set("type", "tcp")
	q.Set("security", "reality")
	q.Set("sni", entry.SNI)
	q.Set("fp", entry.Fingerprint)
	q.Set("pbk", entry.PublicKey)
	sid := u.ShortID
	if sid == "" && len(entry.ShortIDs) > 0 {
		sid = entry.ShortIDs[0]
	}
	q.Set("sid", sid)
	// Vision flow only for non-desktop profiles (mirrors the inbound clients).
	if u.Profile != "desktop" {
		q.Set("flow", "xtls-rprx-vision")
	}
	q.Set("spx", "/")

	label := u.Email
	if label == "" {
		label = u.UUID
	}

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		u.UUID,
		entry.Host,
		entry.Port,
		q.Encode(),
		url.PathEscape(label),
	)
}

// Stream builds the base64 subscription body for a set of users on one entry.
// The body is the standard base64 (std alphabet) of the joined links, which is
// the format every mainstream client expects from a subscription URL.
func Stream(entry config.EntryConfig, users []store.User) string {
	lines := make([]string, 0, len(users))
	for _, u := range users {
		if !u.Enabled {
			continue
		}
		lines = append(lines, Link(entry, u))
	}
	joined := strings.Join(lines, "\n")
	return base64.StdEncoding.EncodeToString([]byte(joined))
}

// MultiStream builds one base64 subscription that spans several entry nodes —
// the main server uses it to hand a client a single link covering every region,
// so the app load-balances/fails over across nodes.
func MultiStream(entries []config.EntryConfig, usersPerEntry map[string][]store.User) string {
	var lines []string
	for _, e := range entries {
		for _, u := range usersPerEntry[e.Host] {
			if !u.Enabled {
				continue
			}
			lines = append(lines, Link(e, u))
		}
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}
