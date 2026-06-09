// Package config defines the on-disk node configuration for vlr and loads/saves
// it as JSON (no YAML dependency, so the binary builds with the stdlib only).
//
// One config file describes one node. Its Role decides which daemon vlr runs:
//
//	standalone — the node owns everything: it terminates Reality, runs the
//	             RU->EU cascade, stores users/metrics locally (SQLite-less, a
//	             JSON state file for now), monitors itself and serves its own
//	             base64 subscription.
//	child      — same data plane, but state is minimal: it pushes heartbeats to
//	             a main server and exposes a pull API the main calls on demand.
//	main       — no data plane. Receives heartbeats, schedules delta-triggered
//	             pulls, aggregates every child and issues subscriptions centrally.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Role is the node's operating mode.
type Role string

const (
	RoleStandalone Role = "standalone"
	RoleChild      Role = "child"
	RoleMain       Role = "main"
)

// DefaultPath is where vlr looks for its config unless --config overrides it.
// On Windows (dev) it falls back to the user config dir.
func DefaultPath() string {
	if p := os.Getenv("VLR_CONFIG"); p != "" {
		return p
	}
	if os.PathSeparator == '/' {
		return "/etc/vlr/config.json"
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "vlr.config.json"
	}
	return filepath.Join(dir, "vlr", "config.json")
}

// Config is the complete node configuration.
type Config struct {
	Role    Role   `json:"role"`
	NodeID  string `json:"node_id"`  // stable id, e.g. ru-yc-msk-01
	Region  string `json:"region"`   // free-form: "RU/Yandex", "EU/Aeza"
	DataDir string `json:"data_dir"` // local state (users.json, metrics)

	Entry   EntryConfig   `json:"entry"`   // client-facing Reality inbound (RU)
	Cascade CascadeConfig `json:"cascade"` // RU->EU WireGuard hop
	Child   ChildConfig   `json:"child"`   // set when Role==child
	Main    MainConfig    `json:"main"`    // set when Role==main
}

// EntryConfig is the public VLESS+Reality inbound that clients connect to.
type EntryConfig struct {
	Host       string   `json:"host"`        // public address clients dial (IP or domain)
	Port       int      `json:"port"`        // usually 443
	SNI        string   `json:"sni"`         // Reality target SNI (real TLS1.3 host)
	Dest       string   `json:"dest"`        // Reality dest, usually "<sni>:443"
	PrivateKey string   `json:"private_key"` // Reality x25519 private (server)
	PublicKey  string   `json:"public_key"`  // Reality x25519 public (clients)
	ShortIDs   []string `json:"short_ids"`   // accepted short IDs

	// Fingerprint advertised to clients. Default "randomized" — see
	// internal/reality/fingerprint.go for why not "chrome".
	Fingerprint string `json:"fingerprint"`
}

// CascadeConfig describes the RU(entry) -> EU(exit) inner hop.
//
// Transport is WireGuard: kernel-space, UDP, carries HTTP/3 and all UDP traffic
// (which SOCKS5-over-SSH cannot), with the lowest inter-DC overhead. The entry
// node routes client egress into the WG tunnel; the EU peer performs the actual
// internet egress. No Reality is needed on this hop — it is datacenter-to-
// datacenter, where camouflage buys nothing and pure speed wins.
type CascadeConfig struct {
	Enabled bool `json:"enabled"`

	// Interface on the entry (RU) side.
	Interface     string `json:"interface"`       // e.g. wg-cascade
	Address       string `json:"address"`         // entry tunnel IP, e.g. 10.66.0.2/32
	PrivateKey    string `json:"private_key"`     // entry WG private key
	ListenPort    int    `json:"listen_port"`     // entry WG listen port
	MTU           int    `json:"mtu"`             // 1420 default; tune for QUIC
	ExitPublicKey string `json:"exit_public_key"` // EU peer public key
	ExitEndpoint  string `json:"exit_endpoint"`   // EU peer host:port
	ExitAllowedIP string `json:"exit_allowed_ip"` // usually 0.0.0.0/0, ::/0
	ExitTunnelIP  string `json:"exit_tunnel_ip"`  // EU tunnel IP, e.g. 10.66.0.1
	Keepalive     int    `json:"keepalive"`       // persistent keepalive secs
}

// ChildConfig is used when Role==child.
type ChildConfig struct {
	MainURL          string `json:"main_url"`          // https://main.example/v1
	Token            string `json:"token"`             // JWT/bearer for this node
	HeartbeatSeconds int    `json:"heartbeat_seconds"` // cheap push interval (15-30)
	PullListen       string `json:"pull_listen"`       // bind for the pull API, e.g. 127.0.0.1:9777
	PullBearer       string `json:"pull_bearer"`       // token main must present to pull
}

// MainConfig is used when Role==main.
type MainConfig struct {
	APIListen       string `json:"api_listen"`        // e.g. 0.0.0.0:8443
	PostgresDSN     string `json:"postgres_dsn"`      // optional; empty => in-memory
	DownAfterMisses int    `json:"down_after_misses"` // missed heartbeats => DOWN
	// PullThreshold is the traffic-counter delta (bytes) that triggers a full
	// pull from a child even if config_version did not change.
	PullThreshold int64 `json:"pull_threshold"`
	// ReconcileSeconds forces a full pull from every child on this interval
	// regardless of deltas (a safety net for missed triggers).
	ReconcileSeconds int `json:"reconcile_seconds"`
}

// Load reads and validates a config from disk.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &c, nil
}

// Save writes the config atomically (write temp + rename) with 0600 perms,
// because it contains private keys.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// Validate checks role-specific invariants.
func (c *Config) Validate() error {
	switch c.Role {
	case RoleStandalone, RoleChild, RoleMain:
	default:
		return fmt.Errorf("unknown role %q", c.Role)
	}
	if c.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	if c.Role == RoleMain {
		if c.Main.APIListen == "" {
			return fmt.Errorf("main.api_listen is required for role=main")
		}
		return nil
	}
	// data-plane roles (standalone, child)
	if c.Entry.Host == "" {
		return fmt.Errorf("entry.host is required")
	}
	if c.Entry.Port == 0 {
		c.Entry.Port = 443
	}
	if c.Entry.PrivateKey == "" || c.Entry.PublicKey == "" {
		return fmt.Errorf("entry reality keys are required (run `vlr init`)")
	}
	if len(c.Entry.ShortIDs) == 0 {
		return fmt.Errorf("entry.short_ids must have at least one entry")
	}
	if c.Entry.Fingerprint == "" {
		return fmt.Errorf("entry.fingerprint is required")
	}
	if c.Role == RoleChild {
		if c.Child.MainURL == "" {
			return fmt.Errorf("child.main_url is required for role=child")
		}
		if c.Child.HeartbeatSeconds == 0 {
			c.Child.HeartbeatSeconds = 20
		}
	}
	return nil
}
