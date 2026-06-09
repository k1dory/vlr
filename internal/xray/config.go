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

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

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
			"email": u.Email,
		}
		// Vision only for mobile profiles. Desktop gets plain VLESS+Reality —
		// XTLS Vision has shown throughput regressions on desktop clients, and
		// the operator explicitly wants desktop to skip it.
		if u.Profile != "desktop" {
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
			// All client egress leaves through freedom. The host's routing table
			// (set up by `vlr up` / wg-quick) sends this out via wg-cascade to EU.
			map[string]any{
				"tag":      "egress",
				"protocol": "freedom",
				"settings": map[string]any{"domainStrategy": "UseIPv4"},
			},
			map[string]any{
				"tag":      "block",
				"protocol": "blackhole",
			},
		},
		"routing": map[string]any{
			"rules": []any{
				map[string]any{
					"type":        "field",
					"inboundTag":  []string{"api-in"},
					"outboundTag": "api",
				},
				// Drop private ranges so a compromised client can't pivot into
				// the EU exit's internal network through the tunnel.
				map[string]any{
					"type":        "field",
					"ip":          []string{"geoip:private"},
					"outboundTag": "block",
				},
			},
		},
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal xray config: %w", err)
	}
	return b, nil
}
