package xray

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

// Apply renders the Xray config for the given users and applies it to a running
// Xray: it writes config.Xray.ConfigPath and reloads the service. This is what
// makes `vlr user add` take effect immediately — no manual render+restart.
//
// It is a no-op (nil) when ConfigPath is empty (Xray auto-apply not configured)
// so callers can always call it. Errors are returned but callers usually treat
// them as best-effort warnings.
func Apply(c *config.Config, users []store.User) error {
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
	if err := os.WriteFile(c.Xray.ConfigPath, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", c.Xray.ConfigPath, err)
	}
	return reload(c.Xray.ReloadCmd)
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
