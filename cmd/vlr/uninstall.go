package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/k1dory/vlr/internal/cascade"
	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/ledger"
)

// cmdLedger lets install.sh (and the user) record/inspect the install ledger.
func cmdLedger(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vlr ledger record --kind X --target Y [--meta k=v,...] | list")
	}
	sub, rest := args[0], args[1:]
	fs := newFlagSet("ledger")
	cfgPath := fs.String("config", "", "config path")
	kind := fs.String("kind", "", "resource kind")
	target := fs.String("target", "", "resource target")
	metaCSV := fs.String("meta", "", "comma-separated k=v metadata")
	_ = fs.Parse(rest)

	lp := ledger.DefaultPath(filepath.Dir(resolveConfigPath(*cfgPath)))
	switch sub {
	case "record":
		if *kind == "" || *target == "" {
			return fmt.Errorf("--kind and --target are required")
		}
		meta := map[string]string{}
		for kv := range strings.SplitSeq(*metaCSV, ",") {
			if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
				meta[k] = v
			}
		}
		return ledger.Record(lp, *kind, *target, meta)
	case "list":
		ents, err := ledger.Load(lp)
		if err != nil {
			return err
		}
		for _, e := range ledger.Dedup(ents) {
			fmt.Printf("%-16s %s %v\n", e.Kind, e.Target, e.Meta)
		}
		return nil
	default:
		return fmt.Errorf("unknown ledger subcommand %q", sub)
	}
}

func resolveConfigPath(p string) string {
	if p == "" {
		return config.DefaultPath()
	}
	return p
}

// cmdUninstall reverses everything vlr installed, in safe order, idempotently.
// Declarative: it combines the ledger ("what I did") with the config ("what
// should exist"), so it works even if the ledger was lost.
func cmdUninstall(args []string) error {
	fs := newFlagSet("uninstall")
	cfgPath := fs.String("config", "", "config path")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	keepConfig := fs.Bool("keep-config", false, "keep /etc/vlr (config, state, ledger)")
	removeGo := fs.Bool("remove-go", false, "also remove Go if vlr installed it")
	removePkgs := fs.Bool("remove-packages", false, "also apt-remove packages vlr installed")
	skipEU := fs.Bool("skip-eu", false, "do not touch the remote EU exit")
	euKey := fs.String("eu-key", "", "SSH key for EU teardown (overrides ledger)")
	euPass := fs.String("eu-pass", "", "SSH password for EU teardown")
	_ = fs.Parse(args)

	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		fmt.Println("⚠ не root — большинство шагов потребуют sudo и могут не сработать")
	}

	cfgFile := resolveConfigPath(*cfgPath)
	cfgDir := filepath.Dir(cfgFile)
	lp := ledger.DefaultPath(cfgDir)
	ents, _ := ledger.Load(lp)
	ents = ledger.Dedup(ents)

	// Best-effort config load for the declarative fallback.
	cfg, _ := config.Load(cfgFile)

	// Collect resources from ledger.
	var files, dirs, ifaces, units, enabled, pkgs []string
	var goInstalled bool
	type euExit struct{ host, user, port, iface, key string }
	var eus []euExit
	for _, e := range ents {
		switch e.Kind {
		case ledger.KindFile:
			files = append(files, e.Target)
		case ledger.KindDir:
			dirs = append(dirs, e.Target)
		case ledger.KindWGIface:
			ifaces = append(ifaces, e.Target)
		case ledger.KindUnit:
			units = append(units, e.Target)
		case ledger.KindEnabledUnit:
			enabled = append(enabled, e.Target)
		case ledger.KindPackage:
			pkgs = append(pkgs, e.Target)
		case ledger.KindGo:
			goInstalled = true
		case ledger.KindEUExit:
			eus = append(eus, euExit{
				host: e.Target, user: e.Meta["user"], port: e.Meta["port"],
				iface: e.Meta["iface"], key: e.Meta["key_path"],
			})
		}
	}

	// Declarative fallbacks (so uninstall works without a ledger).
	addUniq := func(s []string, v string) []string {
		if v == "" || slices.Contains(s, v) {
			return s
		}
		return append(s, v)
	}
	binPath := "/usr/local/bin/vlr"
	unitPath := "/etc/systemd/system/vlr.service"
	files = addUniq(files, binPath)
	units = addUniq(units, unitPath)
	enabled = addUniq(enabled, "vlr")
	dirs = addUniq(dirs, cfgDir)
	if cfg != nil && cfg.Cascade.Interface != "" {
		ifaces = addUniq(ifaces, cfg.Cascade.Interface)
	}
	if len(eus) == 0 && cfg != nil && cfg.Cascade.ExitEndpoint != "" {
		host := cfg.Cascade.ExitEndpoint
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		eus = append(eus, euExit{host: host, user: "root", port: "22", iface: cfg.Cascade.Interface})
	}

	// Plan summary + confirmation.
	fmt.Println("vlr uninstall — будет отменено:")
	if !*skipEU {
		for _, e := range eus {
			fmt.Printf("  • EU-выход %s (wg %s) — снос по SSH\n", e.host, e.iface)
		}
	}
	for _, i := range ifaces {
		fmt.Printf("  • локальный туннель %s (wg-quick down, disable, удалить conf)\n", i)
	}
	fmt.Printf("  • сервис vlr (disable --now), unit %s\n", unitPath)
	fmt.Printf("  • бинарь %s\n", binPath)
	if goInstalled && *removeGo {
		fmt.Println("  • Go-тулчейн /usr/local/go (ставился vlr)")
	}
	if len(pkgs) > 0 && *removePkgs {
		fmt.Printf("  • пакеты: %s\n", strings.Join(pkgs, " "))
	}
	if *keepConfig {
		fmt.Printf("  • КОНФИГ СОХРАНЯЕТСЯ: %s\n", cfgDir)
	} else {
		fmt.Printf("  • %s (конфиг, состояние, журнал)\n", cfgDir)
	}
	if !*yes {
		if !isInteractive() {
			return fmt.Errorf("нужно подтверждение: добавь --yes")
		}
		if ans := strings.ToLower(ask("Продолжить? (yes/no)", "no")); ans != "yes" && ans != "y" {
			fmt.Println("отменено")
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// A. stop the daemon first.
	step("останавливаю сервис vlr")
	sys("systemctl", "disable", "--now", "vlr")

	// B. tear down the remote EU exit(s).
	if !*skipEU {
		for _, e := range eus {
			key := *euKey
			if key == "" {
				key = e.key
			}
			if key == "" && *euPass == "" {
				fmt.Printf("  ⚠ EU %s: нет ключа/пароля — пропускаю (используй --eu-key/--eu-pass или --skip-eu)\n", e.host)
				continue
			}
			port := 22
			fmt.Sscanf(e.port, "%d", &port)
			step("сношу EU-выход " + e.host)
			out, err := cascade.TeardownExit(ctx,
				cascade.SSHOpts{Host: e.host, Port: port, User: orDefault(e.user, "root"), KeyPath: key, Password: *euPass},
				e.iface)
			if err != nil {
				fmt.Printf("  ⚠ EU teardown %s не удался: %v\n%s\n", e.host, err, out)
			}
		}
	}

	// C. local WireGuard interfaces.
	for _, i := range ifaces {
		step("опускаю локальный туннель " + i)
		sys("wg-quick", "down", i)
		sys("systemctl", "disable", "wg-quick@"+i)
		// best-effort: remove the cascade policy rule if it lingered.
		sys("ip", "rule", "del", "fwmark", fmt.Sprintf("%d", config.CascadeFwmark), "table", fmt.Sprintf("%d", config.CascadeFwmark))
		rmf("/etc/wireguard/" + i + ".conf")
	}

	// D. systemd unit.
	step("удаляю systemd-юнит")
	for _, u := range units {
		rmf(u)
	}
	sys("systemctl", "daemon-reload")
	sys("systemctl", "reset-failed", "vlr")

	// E. Go (opt-in, only if we installed it).
	if goInstalled && *removeGo {
		step("удаляю Go-тулчейн")
		rmrf("/usr/local/go")
		rmf("/etc/profile.d/go.sh")
	}

	// F. packages (opt-in, only ones we installed).
	if len(pkgs) > 0 && *removePkgs {
		step("удаляю пакеты: " + strings.Join(pkgs, " "))
		sys("apt-get", append([]string{"remove", "-y"}, pkgs...)...)
	}

	// G. binary + any other recorded files (skip those under cfgDir; handled by H,
	// or kept entirely with --keep-config). filepath.Rel makes the containment
	// check independent of path separators.
	step("удаляю файлы")
	for _, f := range files {
		if rel, err := filepath.Rel(cfgDir, f); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue // f is inside cfgDir
		}
		rmf(f)
	}

	// H. config dir LAST (contains the ledger we are reading).
	if !*keepConfig {
		step("удаляю " + cfgDir)
		rmrf(cfgDir)
	}

	fmt.Println("\n✓ vlr удалён.")
	if *keepConfig {
		fmt.Printf("конфиг оставлен в %s (переустановка: ./install.sh)\n", cfgDir)
	}
	return nil
}

// --- small helpers ---------------------------------------------------------

func step(msg string) { fmt.Println("==>", msg) }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// sys runs a system command, tolerating failure (idempotent uninstall).
func sys(name string, args ...string) {
	if _, err := exec.LookPath(name); err != nil {
		return
	}
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			fmt.Printf("   (%s: %s)\n", name, firstLine(s))
		}
	}
}

func rmf(path string) {
	if path == "" || path == "/" {
		return
	}
	if err := os.Remove(path); err == nil {
		fmt.Println("   удалён", path)
	}
}

func rmrf(path string) {
	if path == "" || path == "/" || path == "/etc" || path == "/usr" {
		return // guardrail against catastrophic paths
	}
	if err := os.RemoveAll(path); err == nil {
		fmt.Println("   удалён", path)
	}
}

func firstLine(s string) string {
	before, _, _ := strings.Cut(s, "\n")
	return before
}
