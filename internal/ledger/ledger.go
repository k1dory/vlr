// Package ledger is an append-only record of everything the install touched, so
// `vlr uninstall` can reverse it precisely and idempotently. It is the "what did
// I do" half of declarative install/uninstall; the "what should exist" half is
// derived from the node config. Uninstall uses both: the ledger for facts not in
// the config (did *we* install Go / the wireguard package, which EU host), and
// the config for the canonical resource set (so uninstall still works if the
// ledger is lost).
package ledger

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Kinds of recorded resources.
const (
	KindFile        = "file"            // a file we created (binary, unit, conf)
	KindDir         = "dir"             // a directory we created
	KindUnit        = "systemd-unit"    // a systemd unit file we installed
	KindEnabledUnit = "systemd-enabled" // a unit we `systemctl enable`d
	KindWGIface     = "wg-iface"        // a local WireGuard interface we brought up
	KindEUExit      = "eu-exit"         // a remote EU exit we provisioned over SSH
	KindPackage     = "package"         // an OS package WE installed (apt)
	KindGo          = "go-toolchain"    // a Go toolchain WE installed
)

// Entry is one recorded action.
type Entry struct {
	Ts     time.Time         `json:"ts"`
	Kind   string            `json:"kind"`
	Target string            `json:"target"`
	Meta   map[string]string `json:"meta,omitempty"`
}

// DefaultPath returns the ledger path next to the config (/etc/vlr/ledger.jsonl
// on Linux). configPath is the resolved config file path; pass "" to derive the
// default location.
func DefaultPath(configDir string) string {
	if configDir == "" {
		if os.PathSeparator == '/' {
			configDir = "/etc/vlr"
		} else if d, err := os.UserConfigDir(); err == nil {
			configDir = filepath.Join(d, "vlr")
		} else {
			configDir = "."
		}
	}
	return filepath.Join(configDir, "ledger.jsonl")
}

// Record appends one entry. It is best-effort: a failure to record must never
// break the operation being recorded, so callers ignore the error in practice.
func Record(path, kind, target string, meta map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(Entry{Ts: time.Now().UTC(), Kind: kind, Target: target, Meta: meta})
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Load reads all entries. Missing file => empty slice, no error.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Dedup keeps the last entry per (kind,target), preserving first-seen order. An
// action recorded twice (e.g. a re-run install) is reversed once.
func Dedup(in []Entry) []Entry {
	idx := map[string]int{}
	order := []string{}
	for _, e := range in {
		k := e.Kind + "\x00" + e.Target
		if _, ok := idx[k]; !ok {
			order = append(order, k)
		}
		idx[k] = -1 // mark present
	}
	last := map[string]Entry{}
	for _, e := range in {
		last[e.Kind+"\x00"+e.Target] = e
	}
	out := make([]Entry, 0, len(order))
	for _, k := range order {
		out = append(out, last[k])
	}
	return out
}
