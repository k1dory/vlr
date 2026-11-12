package devlimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Xray talks to the local Xray API through its CLI (stdlib-only, like
// cascade.XrayStats). The API inbound must expose StatsService + RoutingService
// and the policy must set statsUserOnline — xray.Render does both.
type Xray struct {
	APIAddr    string // default 127.0.0.1:10085
	Bin        string // default "xray"
	ConfigPath string // live Xray config; its routing.rules are the static rules
}

func (x Xray) addr() string {
	if x.APIAddr != "" {
		return x.APIAddr
	}
	return "127.0.0.1:10085"
}

func (x Xray) bin() string {
	if x.Bin != "" {
		return x.Bin
	}
	return "xray"
}

// Online returns user email -> online source IPs. It uses the one-shot
// `statsonlineiplist -all` (Xray 26.4+) and falls back to listing online users
// and querying each one (older Xray, which lacks -all).
func (x Xray) Online(ctx context.Context) (map[string][]string, error) {
	out, err := exec.CommandContext(ctx, x.bin(), "api", "statsonlineiplist",
		"--server="+x.addr(), "-all").Output()
	if err == nil {
		return parseOnline(out)
	}
	return x.onlinePerUser(ctx)
}

func (x Xray) onlinePerUser(ctx context.Context) (map[string][]string, error) {
	out, err := exec.CommandContext(ctx, x.bin(), "api", "statsgetallonlineusers",
		"--server="+x.addr()).Output()
	if err != nil {
		return nil, fmt.Errorf("xray statsgetallonlineusers: %w", err)
	}
	emails, err := parseOnlineUsers(out)
	if err != nil {
		return nil, err
	}
	m := make(map[string][]string, len(emails))
	for _, e := range emails {
		out, err := exec.CommandContext(ctx, x.bin(), "api", "statsonlineiplist",
			"--server="+x.addr(), "-email", e).Output()
		if err != nil {
			continue // user went offline between the two calls
		}
		ips, err := parseUserIPs(out)
		if err != nil {
			return nil, err
		}
		if len(ips) > 0 {
			m[e] = ips
		}
	}
	return m, nil
}

// parseOnlineUsers: {"users":["user>>>EMAIL>>>online", ...]} -> emails.
func parseOnlineUsers(b []byte) ([]string, error) {
	var resp struct {
		Users []string `json:"users"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("parse online users: %w", err)
	}
	out := make([]string, 0, len(resp.Users))
	for _, u := range resp.Users {
		e := strings.TrimSuffix(strings.TrimPrefix(u, "user>>>"), ">>>online")
		if e != "" && e != u {
			out = append(out, e)
		}
	}
	return out, nil
}

// parseUserIPs: {"ips":{"1.2.3.4":<unix>}, "name":...} -> sorted IPs.
func parseUserIPs(b []byte) ([]string, error) {
	var resp struct {
		IPs map[string]int64 `json:"ips"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("parse user ips: %w", err)
	}
	out := make([]string, 0, len(resp.IPs))
	for ip := range resp.IPs {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out, nil
}

func parseOnline(b []byte) (map[string][]string, error) {
	var resp struct {
		Users []struct {
			Email string `json:"email"`
			IPs   []struct {
				IP string `json:"ip"`
			} `json:"ips"`
		} `json:"users"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("parse online ips: %w", err)
	}
	m := make(map[string][]string, len(resp.Users))
	for _, u := range resp.Users {
		for _, ip := range u.IPs {
			if ip.IP != "" {
				m[u.Email] = append(m[u.Email], ip.IP)
			}
		}
	}
	return m, nil
}

// ApplyBans replaces Xray's routing rules with ban rules followed by the static
// rules from the live config file. Reading the file (not the daemon's in-memory
// config) keeps `vlr split add` edits intact. Replace, not append: ban rules must
// come first to beat the split/egress rules, and a full replace is atomic.
func (x Xray) ApplyBans(ctx context.Context, bans []Ban) error {
	raw, err := os.ReadFile(x.ConfigPath)
	if err != nil {
		return fmt.Errorf("read xray config: %w", err)
	}
	rules, err := BuildRules(raw, bans)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(x.ConfigPath), ".vlr-rules-*.json")
	if err != nil {
		return fmt.Errorf("temp rules file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(rules); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, x.bin(), "api", "adrules",
		"--server="+x.addr(), tmp.Name()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xray adrules: %w: %s", err, out)
	}
	return nil
}

// BuildRules returns an adrules payload: ban rules, then config's routing.rules.
func BuildRules(xrayConfig []byte, bans []Ban) ([]byte, error) {
	var cfg struct {
		Routing struct {
			Rules []json.RawMessage `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(xrayConfig, &cfg); err != nil {
		return nil, fmt.Errorf("parse xray config: %w", err)
	}
	rules := make([]any, 0, len(bans)+len(cfg.Routing.Rules))
	for _, b := range bans {
		rules = append(rules, map[string]any{
			"type":        "field",
			"ruleTag":     RuleTag(b.User, b.IP),
			"user":        []string{b.User},
			"source":      []string{b.IP},
			"outboundTag": "block",
		})
	}
	for _, r := range cfg.Routing.Rules {
		rules = append(rules, r)
	}
	return json.Marshal(map[string]any{"routing": map[string]any{"rules": rules}})
}

// RuleTag is a stable tag for a ban rule (no raw email in the tag).
func RuleTag(user, ip string) string {
	h := sha256.Sum256([]byte(user + "|" + ip))
	return "vlr-devlimit-" + hex.EncodeToString(h[:6])
}

// Run drives the limiter until ctx is done. Bans are re-pushed every tick while
// any exist, so they survive an Xray restart (e.g. `vlr user add`), and the
// static rules are pushed once when the last ban lifts.
func Run(ctx context.Context, log *slog.Logger, p Policy, x Xray, every time.Duration) {
	if every <= 0 {
		every = 15 * time.Second
	}
	l := New(p)
	t := time.NewTicker(every)
	defer t.Stop()
	log.Info("device limit enabled", "exempt", len(p.Exempt), "max_devices", p.Max, "ban", l.policy.Ban.String(), "every", every.String())
	pushed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		online, err := x.Online(ctx)
		if err != nil {
			log.Warn("device limit: read online ips", "err", err)
			continue
		}
		bans, changed := l.Step(time.Now(), online)
		if changed {
			log.Info("device limit: bans changed", "active", len(bans))
			for _, b := range bans {
				log.Info("device limit: ban", "user", b.User, "ip", b.IP, "until", b.Until.Format(time.RFC3339))
			}
		}
		if len(bans) == 0 && pushed == 0 && !changed {
			continue // nothing to keep in sync
		}
		if err := x.ApplyBans(ctx, bans); err != nil {
			log.Warn("device limit: apply bans", "err", err)
			continue
		}
		pushed = len(bans)
	}
}
