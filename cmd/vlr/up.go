package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/ledger"
	"github.com/k1dory/vlr/internal/store"
	"github.com/k1dory/vlr/internal/xray"
)

// cmdUp brings the data plane up in one command: install Xray-core if missing,
// make sure its service can set SO_MARK (CAP_NET_ADMIN, for the cascade), then
// render the config and reload Xray. After `vlr init` + `vlr cascade up`, this is
// the only step left to a serving node.
func cmdUp(args []string) error {
	fs := newFlagSet("up")
	cfgPath := fs.String("config", "", "config path")
	noInstall := fs.Bool("no-install-xray", false, "don't auto-install Xray-core")
	_ = fs.Parse(args)

	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	if c.Role == config.RoleMain {
		return fmt.Errorf("role=main не обслуживает VPN; `vlr up` для standalone/child")
	}
	st, err := store.Open(dataDir(c))
	if err != nil {
		return err
	}
	lp := ledger.DefaultPath(filepath.Dir(resolveConfigPath(*cfgPath)))

	// 1. Install Xray-core if absent.
	if _, e := exec.LookPath("xray"); e != nil {
		if *noInstall {
			return fmt.Errorf("xray не установлен (убери --no-install-xray или поставь вручную)")
		}
		if err := installXray(); err != nil {
			return err
		}
		_ = ledger.Record(lp, ledger.KindPackage, "xray", nil)
	} else {
		fmt.Println("==> Xray уже установлен")
	}

	// 2. Ensure the Xray service may set SO_MARK (needed to mark EU-bound traffic
	// into the cascade). A systemd drop-in is non-destructive.
	ensureXrayNetAdmin(lp)

	// 3. Render + reload.
	if c.Xray.ConfigPath == "" {
		c.Xray.ConfigPath = "/usr/local/etc/xray/config.json"
	}
	if err := xray.Apply(c, st.Users()); err != nil {
		return fmt.Errorf("apply xray: %w", err)
	}
	fmt.Println("✓ Xray поднят:", c.Xray.ConfigPath)
	fmt.Printf("  юзеров: %d. Добавить: vlr user add  (или POST /v1/users)\n", len(st.Users()))
	return nil
}

func installXray() error {
	fmt.Println("==> ставлю Xray-core (official installer)")
	const url = "https://github.com/XTLS/Xray-install/raw/main/install-release.sh"
	if err := exec.Command("bash", "-c",
		"curl -fsSL "+url+" -o /tmp/xray-install.sh && bash /tmp/xray-install.sh install").Run(); err != nil {
		return fmt.Errorf("Xray install failed: %w (поставь вручную)", err)
	}
	return nil
}

// ensureXrayNetAdmin writes a systemd drop-in granting CAP_NET_ADMIN to xray, so
// Xray's sockopt.mark works. Best-effort; needs systemd.
func ensureXrayNetAdmin(lp string) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return
	}
	dir := "/etc/systemd/system/xray.service.d"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	override := filepath.Join(dir, "10-vlr-netadmin.conf")
	content := "[Service]\n" +
		"AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE\n" +
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE\n"
	if err := os.WriteFile(override, []byte(content), 0o644); err != nil {
		return
	}
	_ = ledger.Record(lp, ledger.KindFile, override, nil)
	_ = exec.Command("systemctl", "daemon-reload").Run()
	fmt.Println("==> Xray: выдан CAP_NET_ADMIN (drop-in)")
}
