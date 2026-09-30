package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallLogBoundsFailureOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	cmd := exec.Command("sh", "-c", "cat; exit 7")
	payload := strings.Repeat("package installation output\n", 10000)
	cmd.Stdin = strings.NewReader(payload)
	err := runInstallLogged(cmd)
	if err == nil || len(err.Error()) > 5000 {
		t.Fatalf("expected bounded error, got %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(dir, "vlr-install-*.log"))
	if len(logs) != 1 {
		t.Fatalf("logs: %v", logs)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil || string(data) != payload {
		t.Fatal("full install output was not preserved")
	}
	info, err := os.Stat(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("log permissions: %o", info.Mode().Perm())
	}
}
