package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/flock"
	"github.com/k1dory/vlr/internal/store"
)

// InboundTag is the client-facing VLESS+Reality inbound.
const InboundTag = "vless-reality"

// APIAddr is the loopback Xray API (the api-in inbound).
const APIAddr = "127.0.0.1:10085"

// UserSource yields the authoritative user list. Apply reads it only after
// taking the apply lock, so a stale snapshot can never re-add a revoked user.
type UserSource interface {
	Users() []store.User
}

var applyMu sync.Mutex // serializes Apply within one process; flock covers processes

// Apply brings the running Xray in line with the store, as gently as possible:
//
//   - the non-client part of the rendered config equals the file, and the
//     running Xray's users (read back over its API) equal the rendered ones →
//     at most the file is rewritten; nothing touches Xray;
//   - only users differ → they are removed/added on the live Xray (HandlerService
//     rmu/adu), the live user list is read back and must equal the rendered one,
//     then the file is written. No restart, nobody's connection drops;
//   - anything else — other config changed, no file, Xray down, API error, or
//     the read-back does not match — write the file and restart Xray.
//
// The comparison is against what Xray actually runs, not against the file, so a
// failed earlier write or a silently failed RPC is repaired on the next Apply.
// A restart that did not complete leaves a .pending marker that forces the next
// Apply to restart again. No-op when ConfigPath is empty.
func Apply(c *config.Config, src UserSource) error { return apply(c, src, false) }

// ApplyForce always writes the config and restarts Xray (`vlr up`).
func ApplyForce(c *config.Config, src UserSource) error { return apply(c, src, true) }

func apply(c *config.Config, src UserSource, force bool) error {
	if c.Xray.ConfigPath == "" {
		return nil // auto-apply disabled
	}
	if err := os.MkdirAll(filepath.Dir(c.Xray.ConfigPath), 0o755); err != nil {
		return fmt.Errorf("mkdir xray config dir: %w", err)
	}
	applyMu.Lock()
	defer applyMu.Unlock()
	unlock, err := flock.Lock(c.Xray.ConfigPath + ".lock")
	if err != nil {
		return fmt.Errorf("lock xray config: %w", err)
	}
	defer unlock()

	b, err := Render(c, src.Users()) // snapshot taken under the lock
	if err != nil {
		return fmt.Errorf("render xray config: %w", err)
	}
	pending := c.Xray.ConfigPath + ".pending"
	if _, err := os.Stat(pending); err == nil {
		force = true // a previous restart did not complete
	}
	if !force {
		if done, err := tryLive(c.Xray.ConfigPath, b); done {
			return err
		}
	}
	return restartWith(c, b, pending)
}

// tryLive applies b without a restart if possible. done=false means "fall back
// to a restart"; done=true returns err as the Apply result.
func tryLive(path string, b []byte) (done bool, err error) {
	old, rerr := os.ReadFile(path)
	if rerr != nil {
		return false, nil
	}
	same, perr := SameExceptClients(old, b)
	if perr != nil || !same {
		return false, nil
	}
	desired, derr := renderedClients(b)
	if derr != nil {
		return false, nil
	}
	live, lerr := liveClients()
	if lerr != nil {
		return false, nil // Xray down or API off
	}
	p := Diff(live, desired)
	if !p.Empty() {
		if err := applyLive(p); err != nil {
			return false, nil
		}
		after, err := liveClients()
		if err != nil || !reflect.DeepEqual(after, desired) {
			return false, nil // partial/silent RPC failure: reconcile by restart
		}
	}
	if string(old) != string(b) {
		// Xray already runs b's users; if this write fails the next Apply still
		// compares against the live list, so nothing is lost.
		if err := writeAtomic(path, b); err != nil {
			return true, err
		}
	}
	return true, nil
}

func restartWith(c *config.Config, b []byte, pending string) error {
	if err := os.WriteFile(pending, nil, 0o600); err != nil {
		return fmt.Errorf("mark pending: %w", err)
	}
	if err := writeAtomic(c.Xray.ConfigPath, b); err != nil {
		return err
	}
	if err := reload(c.Xray.ReloadCmd); err != nil {
		return err // marker stays: next Apply restarts again
	}
	_ = os.Remove(pending)
	return nil
}

// Client is the part of a VLESS client that matters for authorization.
type Client struct {
	ID   string
	Flow string
}

// LivePlan is what to change on the running Xray.
type LivePlan struct {
	Remove []string         // emails to drop (gone or changed)
	Add    []map[string]any // client objects to add (new or changed)
}

// Empty reports nothing to change.
func (p LivePlan) Empty() bool { return len(p.Remove) == 0 && len(p.Add) == 0 }

// Diff computes the live changes turning `live` into `desired`. Pure.
func Diff(live, desired map[string]Client) LivePlan {
	var p LivePlan
	for email, lc := range live {
		if dc, ok := desired[email]; !ok || dc != lc {
			p.Remove = append(p.Remove, email)
		}
	}
	for email, dc := range desired {
		if lc, ok := live[email]; !ok || dc != lc {
			cl := map[string]any{"id": dc.ID, "email": email}
			if dc.Flow != "" {
				cl["flow"] = dc.Flow
			}
			p.Add = append(p.Add, cl)
		}
	}
	sort.Strings(p.Remove)
	sort.Slice(p.Add, func(i, j int) bool { return p.Add[i]["email"].(string) < p.Add[j]["email"].(string) })
	return p
}

// SameExceptClients reports whether two configs differ only in the VLESS
// inbound's client list. Errors on configs that are not safe to patch live.
func SameExceptClients(a, b []byte) (bool, error) {
	ra, _, err := splitClients(a)
	if err != nil {
		return false, err
	}
	rb, _, err := splitClients(b)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(ra, rb), nil
}

func renderedClients(b []byte) (map[string]Client, error) {
	_, raw, err := splitClients(b)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Client, len(raw))
	for email, cl := range raw {
		id, _ := cl["id"].(string)
		flow, _ := cl["flow"].(string)
		out[email] = Client{ID: id, Flow: flow}
	}
	return out, nil
}

// splitClients returns the config with the VLESS inbound's clients removed and
// those clients keyed by email. Fails if the inbound is missing or a client
// email is empty or duplicated.
func splitClients(raw []byte) (map[string]any, map[string]map[string]any, error) {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, err
	}
	ins, _ := cfg["inbounds"].([]any)
	for _, in := range ins {
		inb, _ := in.(map[string]any)
		if inb["tag"] != InboundTag {
			continue
		}
		settings, _ := inb["settings"].(map[string]any)
		if settings == nil {
			return nil, nil, fmt.Errorf("inbound %s has no settings", InboundTag)
		}
		list, _ := settings["clients"].([]any)
		clients := make(map[string]map[string]any, len(list))
		for _, x := range list {
			cl, _ := x.(map[string]any)
			email, _ := cl["email"].(string)
			if email == "" {
				return nil, nil, fmt.Errorf("client without email")
			}
			if _, dup := clients[email]; dup {
				return nil, nil, fmt.Errorf("duplicate client email %q", email)
			}
			clients[email] = cl
		}
		delete(settings, "clients")
		return cfg, clients, nil
	}
	return nil, nil, fmt.Errorf("inbound %s not found", InboundTag)
}

// runXray is the CLI runner; a variable so tests can substitute a fake Xray.
var runXray = func(args ...string) ([]byte, error) {
	out, err := exec.Command("xray", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("xray %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// liveClients reads the running inbound's users back from Xray.
func liveClients() (map[string]Client, error) {
	out, err := runXray("api", "inbounduser", "--server="+APIAddr, "-tag="+InboundTag)
	if err != nil {
		return nil, err
	}
	return parseLiveUsers(out)
}

func parseLiveUsers(out []byte) (map[string]Client, error) {
	var resp struct {
		Users []struct {
			Email   string `json:"email"`
			Account struct {
				ID   string `json:"id"`
				Flow string `json:"flow"`
			} `json:"account"`
		} `json:"users"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse inbounduser: %w", err)
	}
	m := make(map[string]Client, len(resp.Users))
	for _, u := range resp.Users {
		if u.Email == "" {
			continue // Xray's empty placeholder entry
		}
		m[u.Email] = Client{ID: u.Account.ID, Flow: u.Account.Flow}
	}
	return m, nil
}

// applyLive removes, then adds users on the running Xray. Success is NOT taken
// from the exit code (rmu/adu report per-user failures and still exit 0); the
// caller verifies by reading the live list back.
func applyLive(p LivePlan) error {
	if len(p.Remove) > 0 {
		args := append([]string{"api", "rmu", "--server=" + APIAddr, "-tag=" + InboundTag}, p.Remove...)
		if _, err := runXray(args...); err != nil {
			return err
		}
	}
	if len(p.Add) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string]any{"inbounds": []any{map[string]any{
		"tag": InboundTag, "protocol": "vless", "port": 0,
		"settings": map[string]any{"clients": p.Add, "decryption": "none"},
	}}})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "vlr-adu-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = runXray("api", "adu", "--server="+APIAddr, f.Name())
	return err
}

// writeAtomic replaces path via a temp file + rename, so Xray never reads a
// half-written config.
func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// reload restarts/reloads Xray. With an explicit ReloadCmd it runs that; else it
// tries `systemctl reload xray` and falls back to `systemctl restart xray`.
func reload(cmd string) error {
	if cmd != "" {
		parts := strings.Fields(cmd)
		return runQuiet(parts[0], parts[1:]...)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil // no systemd (dev/Windows) — config written, nothing to reload
	}
	if runQuiet("systemctl", "reload", "xray") == nil {
		return nil
	}
	return runQuiet("systemctl", "restart", "xray")
}

func runQuiet(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
