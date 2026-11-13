package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

var applyMu sync.Mutex // serializes Apply within one process; flock covers processes

// Apply renders the Xray config for the given users and brings the running Xray
// in line with it, as gently as possible:
//
//   - rendered config identical to the file on disk → nothing to do;
//   - only the client list of the VLESS inbound changed → users are removed and
//     added on the live Xray through its API (HandlerService: rmu/adu), then the
//     file is written. No restart, so nobody's connection drops;
//   - anything else changed, there is no file yet, or the API call failed →
//     write the file and reload/restart Xray (the old behaviour).
//
// It is a no-op (nil) when ConfigPath is empty (Xray auto-apply not configured).
func Apply(c *config.Config, users []store.User) error {
	return apply(c, users, false)
}

// ApplyForce always writes the config and reloads/restarts Xray (`vlr up`).
func ApplyForce(c *config.Config, users []store.User) error {
	return apply(c, users, true)
}

func apply(c *config.Config, users []store.User, force bool) error {
	if c.Xray.ConfigPath == "" {
		return nil // auto-apply disabled
	}
	b, err := Render(c, users)
	if err != nil {
		return fmt.Errorf("render xray config: %w", err)
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

	if !force {
		if old, rerr := os.ReadFile(c.Xray.ConfigPath); rerr == nil {
			p, perr := PlanLive(old, b)
			switch {
			case perr != nil:
				// unparsable old file: fall through to write+reload
			case p.Noop():
				return nil
			case p.ClientsOnly:
				if lerr := applyLive(p); lerr == nil {
					return writeAtomic(c.Xray.ConfigPath, b)
				}
				// live update failed (Xray down, API off): fall back to a restart
			}
		}
	}
	if err := writeAtomic(c.Xray.ConfigPath, b); err != nil {
		return err
	}
	return reload(c.Xray.ReloadCmd)
}

// LivePlan is the difference between the running (on-disk) config and a newly
// rendered one.
type LivePlan struct {
	// ClientsOnly: everything except the VLESS inbound's client list is equal,
	// so the change can be applied through the API.
	ClientsOnly bool
	Remove      []string         // client emails to drop (gone or changed)
	Add         []map[string]any // client objects to add (new or changed)
}

// Noop reports an identical config.
func (p LivePlan) Noop() bool { return p.ClientsOnly && len(p.Remove) == 0 && len(p.Add) == 0 }

// PlanLive compares two rendered configs. Pure; unit-tested.
func PlanLive(oldCfg, newCfg []byte) (LivePlan, error) {
	oldRest, oldClients, err := splitClients(oldCfg)
	if err != nil {
		return LivePlan{}, fmt.Errorf("old config: %w", err)
	}
	newRest, newClients, err := splitClients(newCfg)
	if err != nil {
		return LivePlan{}, fmt.Errorf("new config: %w", err)
	}
	if !reflect.DeepEqual(oldRest, newRest) {
		return LivePlan{}, nil
	}
	p := LivePlan{ClientsOnly: true}
	for email, oc := range oldClients {
		if nc, ok := newClients[email]; !ok || !reflect.DeepEqual(oc, nc) {
			p.Remove = append(p.Remove, email)
		}
	}
	for email, nc := range newClients {
		if oc, ok := oldClients[email]; !ok || !reflect.DeepEqual(oc, nc) {
			p.Add = append(p.Add, nc)
		}
	}
	sortStrings(p.Remove)
	sortClients(p.Add)
	return p, nil
}

// splitClients returns the config with the VLESS inbound's clients removed, and
// those clients keyed by email. Fails if the inbound or a client email is missing
// or duplicated — such a config is not safe to patch live.
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

// runXray is the CLI runner; a variable so tests can stub it.
var runXray = func(args ...string) error {
	out, err := exec.Command("xray", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xray %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyLive removes, then adds users on the running Xray.
func applyLive(p LivePlan) error {
	if len(p.Remove) > 0 {
		args := append([]string{"api", "rmu", "--server=" + APIAddr, "-tag=" + InboundTag}, p.Remove...)
		if err := runXray(args...); err != nil {
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
	return runXray("api", "adu", "--server="+APIAddr, f.Name())
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
