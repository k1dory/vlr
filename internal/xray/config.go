// Package xray renders an Xray-core JSON config for a vlr entry node.
//
// Topology produced:
//
//	client --VLESS+Reality(+Vision)--> [entry inbound]
//	                                        |
//	                                   routing: all traffic
//	                                        v
//	                                  [freedom outbound]  --> egress
//
// The egress is plain `freedom`. The RU->EU hop is NOT done inside Xray; it is a
// kernel WireGuard interface (see internal/wireguard) into which the host routes
// the freedom outbound's packets. That keeps the cascade UDP-clean (HTTP/3 works)
// and off Xray's userspace datapath. Xray only terminates Reality and accounts
// per-user traffic via the stats API.
package xray

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

// buildRoutingRules assembles the Xray routing table: stats API, then the
// split-tunnel RU-direct rule (domains/geosite -> egress-ru, leave RU directly),
// then a private-range blackhole so a client can't reach the EU exit's LAN. Any
// traffic matching no rule falls to the first outbound (egress -> EU).
func buildRoutingRules(c *config.Config) []any {
	rules := []any{
		map[string]any{
			"type":        "field",
			"inboundTag":  []string{"api-in"},
			"outboundTag": "api",
		},
	}

	var ruMatch []string
	for _, d := range c.Split.RUDirect {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		// Bare host -> "domain:host" (matches host and its subdomains). Explicit
		// Xray matchers (full:, domain:, regexp:, geosite:) are passed through.
		if strings.Contains(d, ":") {
			ruMatch = append(ruMatch, d)
		} else {
			ruMatch = append(ruMatch, "domain:"+d)
		}
	}
	for _, g := range c.Split.GeositeRU {
		if g = strings.TrimSpace(g); g != "" {
			ruMatch = append(ruMatch, "geosite:"+g)
		}
	}
	if len(ruMatch) > 0 {
		rules = append(rules, map[string]any{
			"type":        "field",
			"domain":      ruMatch,
			"outboundTag": "egress-ru",
		})
	}

	// Drop private ranges so a compromised client can't pivot into the EU exit's
	// internal network through the tunnel.
	rules = append(rules, map[string]any{
		"type":        "field",
		"ip":          []string{"geoip:private"},
		"outboundTag": "block",
	})
	return rules
}

// StatID is the per-client identity Xray records traffic under (the inbound
// client's `email`). It must be unique and stable per user: vlr uses the user's
// email when set (AddUser keeps non-empty emails unique), else the UUID. The
// stats poller maps this back via store.UpdateTraffic. Without this, users with
// an empty email would collide under the "" stat key and lose accounting.
func StatID(u store.User) string {
	if u.Email != "" {
		return u.Email
	}
	return u.UUID
}

// Render returns the Xray config JSON for the given node config + user list.
func Render(c *config.Config, users []store.User) ([]byte, error) {
	if c.Entry.Dest == "" {
		c.Entry.Dest = c.Entry.SNI + ":443"
	}

	clients := make([]map[string]any, 0, len(users))
	for _, u := range users {
		if !u.Enabled {
			continue
		}
		cl := map[string]any{
			"id":    u.UUID,
			"email": StatID(u), // stable, UNIQUE per-client stat identity
		}
		// Vision is OPT-IN (profile=="vision"), because XTLS-Vision breaks many
		// desktop (Windows/macOS) clients and Xray pins the flow per UUID, so one
		// credential can't be vision on a phone and plain on a PC. Default = plain
		// VLESS+Reality, which works on every client and still resists RU DPI.
		if u.Profile == "vision" {
			cl["flow"] = "xtls-rprx-vision"
		}
		clients = append(clients, cl)
	}

	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		// Stats + policy enable per-email up/down counters that the poller reads
		// to fill store.User.{Rx,Tx}Bytes — the basis of the cheap heartbeat.
		"stats": map[string]any{},
		"policy": map[string]any{
			"levels": map[string]any{
				"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true},
			},
			"system": map[string]any{
				"statsInboundUplink":   true,
				"statsInboundDownlink": true,
			},
		},
		"api": map[string]any{
			"tag":      "api",
			"services": []string{"StatsService"},
		},
		"inbounds": []any{
			map[string]any{
				"tag":      "vless-reality",
				"listen":   "0.0.0.0",
				"port":     c.Entry.Port,
				"protocol": "vless",
				"settings": map[string]any{
					"clients":    clients,
					"decryption": "none",
				},
				"streamSettings": map[string]any{
					"network":  "tcp",
					"security": "reality",
					"realitySettings": map[string]any{
						"show":        false,
						"dest":        c.Entry.Dest,
						"xver":        0,
						"serverNames": []string{c.Entry.SNI},
						"privateKey":  c.Entry.PrivateKey,
						"shortIds":    c.Entry.ShortIDs,
					},
				},
				"sniffing": map[string]any{
					"enabled":      true,
					"destOverride": []string{"http", "tls", "quic"},
				},
			},
			// Local stats API inbound (dnet) — bound to loopback only.
			map[string]any{
				"tag":      "api-in",
				"listen":   "127.0.0.1",
				"port":     10085,
				"protocol": "dokodemo-door",
				"settings": map[string]any{"address": "127.0.0.1"},
			},
		},
		"outbounds": []any{
			// DEFAULT egress (-> EU): freedom MARKED with the cascade fwmark. The
			// `ip rule fwmark` policy routes only marked traffic into wg-cascade,
			// so this is the only traffic that cascades to the EU exit. Requires
			// Xray to run with CAP_NET_ADMIN (it runs as root by default) to set
			// SO_MARK.
			map[string]any{
				"tag":      "egress",
				"protocol": "freedom",
				"settings": map[string]any{"domainStrategy": "UseIPv4"},
				"streamSettings": map[string]any{
					"sockopt": map[string]any{"mark": config.CascadeFwmark},
				},
			},
			// SPLIT-TUNNEL egress (-> direct RU): unmarked freedom. Unmarked means
			// the main routing table, i.e. straight out the RU node's NIC. Used for
			// the RU-direct domain list.
			map[string]any{
				"tag":      "egress-ru",
				"protocol": "freedom",
				"settings": map[string]any{"domainStrategy": "UseIPv4"},
			},
			map[string]any{
				"tag":      "block",
				"protocol": "blackhole",
			},
		},
		"routing": map[string]any{
			"rules": buildRoutingRules(c),
		},
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal xray config: %w", err)
	}
	return b, nil
}
