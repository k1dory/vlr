package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// runInstallLogged keeps package-manager output off the terminal. Logs remain
// private (0600), including on failure; only a bounded tail is printed.
func runInstallLogged(cmd *exec.Cmd) error {
	f, err := os.CreateTemp("", "vlr-install-*.log")
	if err != nil {
		return fmt.Errorf("create install log: %w", err)
	}
	defer f.Close()
	fmt.Printf("    лог установки: %s\n", f.Name())
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Run(); err != nil {
		info, statErr := f.Stat()
		if statErr != nil {
			return fmt.Errorf("installation: %w (log: %s)", err, f.Name())
		}
		start := max(int64(0), info.Size()-4096)
		_, _ = f.Seek(start, io.SeekStart)
		tail, _ := io.ReadAll(io.LimitReader(f, 4096))
		lines := strings.Split(strings.TrimSpace(string(tail)), "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		return fmt.Errorf("installation: %w (log: %s)\n%s", err, f.Name(), strings.Join(lines, "\n"))
	}
	return nil
}
