package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The daemon selects wg/awg once at startup. Refresh it after migration, but
// only when its running process uses the config that this command changed.
func refreshCascadeDaemon(configPath string) error {
	if exec.Command("systemctl", "is-active", "--quiet", "vlr").Run() != nil {
		return nil
	}
	pid, err := exec.Command("systemctl", "show", "--property=MainPID", "--value", "vlr").Output()
	if err != nil {
		return fmt.Errorf("inspect vlr service: %w", err)
	}
	cmdline, err := os.ReadFile(filepath.Join("/proc", strings.TrimSpace(string(pid)), "cmdline"))
	if err != nil {
		return fmt.Errorf("read vlr command line: %w", err)
	}
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	runningConfig := "/etc/vlr/config.json"
	for i, arg := range args {
		if (arg == "--config" || arg == "-config") && i+1 < len(args) {
			runningConfig = args[i+1]
		}
		if value, ok := strings.CutPrefix(arg, "--config="); ok {
			runningConfig = value
		}
		if value, ok := strings.CutPrefix(arg, "-config="); ok {
			runningConfig = value
		}
	}
	want, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	got, err := os.Stat(runningConfig)
	if err != nil || !os.SameFile(want, got) {
		fmt.Println("→ vlr использует другой конфиг; перезапусти соответствующий сервис после миграции")
		return nil
	}
	if out, err := exec.Command("systemctl", "restart", "vlr").CombinedOutput(); err != nil {
		return fmt.Errorf("restart vlr after transport migration: %w\n%s", err, out)
	}
	fmt.Println("✓ vlr перечитал настройки туннеля")
	return nil
}
