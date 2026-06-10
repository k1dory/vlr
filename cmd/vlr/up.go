package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	_ = exec.Command("systemctl", "enable", "xray").Run()
	_ = exec.Command("systemctl", "restart", "xray").Run()
	fmt.Println("✓ Xray применён:", c.Xray.ConfigPath)

	verifyXray(c)
	fmt.Printf("\n  юзеров: %d. Добавить: vlr user add  (или POST /v1/users)\n", len(st.Users()))
	return nil
}

// verifyXray runs quick self-checks and prints what's wrong, so "ничего не
// работает" becomes a concrete diagnosis instead of a guess.
func verifyXray(c *config.Config) {
	fmt.Println("\n== диагностика ==")

	// 1. config valid?
	if _, err := exec.LookPath("xray"); err == nil {
		if out, err := exec.Command("xray", "-test", "-c", c.Xray.ConfigPath).CombinedOutput(); err != nil {
			fmt.Printf("✗ конфиг Xray невалиден:\n%s\n", string(out))
		} else {
			fmt.Println("✓ конфиг Xray валиден")
		}
	}

	// 2. service active?
	if out, _ := exec.Command("systemctl", "is-active", "xray").Output(); strings.TrimSpace(string(out)) == "active" {
		fmt.Println("✓ сервис xray запущен")
	} else {
		fmt.Println("✗ сервис xray НЕ активен — смотри: journalctl -u xray -n 30 --no-pager")
	}

	// 3. listening on the entry port?
	port := c.Entry.Port
	if port == 0 {
		port = 443
	}
	listening := false
	if out, err := exec.Command("ss", "-tlnH").Output(); err == nil {
		listening = strings.Contains(string(out), fmt.Sprintf(":%d ", port))
	}
	if listening {
		fmt.Printf("✓ Xray слушает :%d\n", port)
	} else {
		fmt.Printf("✗ никто не слушает :%d (Xray не стартовал?)\n", port)
	}

	// 4. firewall reminder — the #1 cause on cloud VMs.
	fmt.Printf("→ проверь, что входящий TCP %d открыт в Security Group/файрволе провайдера\n", port)
	fmt.Printf("  (Yandex Cloud: Консоль → Сети → Группы безопасности → разрешить TCP %d из 0.0.0.0/0)\n", port)
	fmt.Printf("  снаружи: nc -vz %s %d\n", c.Entry.Host, port)
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
