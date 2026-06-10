package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/k1dory/vlr/internal/cascade"
	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/ledger"
	"github.com/k1dory/vlr/internal/wireguard"
)

// cmdCascadeUp provisions the EU exit from the RU node in one shot and brings the
// tunnel up. This is the automated equivalent of Genomed-mtproto's `mtg kaskad`:
// generate the RU key, SSH into EU (key or password), stand up a forward-only
// WireGuard exit there, wire both sides, then healthcheck through the cascade.
func cmdCascadeUp(args []string) error {
	fs := newFlagSet("cascade up")
	cfgPath := fs.String("config", "", "config path")
	euHost := fs.String("eu-host", "", "EU exit host/IP (required)")
	euPort := fs.Int("eu-port", 22, "EU SSH port")
	euUser := fs.String("eu-user", "root", "EU SSH user")
	euKey := fs.String("eu-key", "", "SSH private key path (key auth)")
	euPass := fs.String("eu-pass", "", "SSH password (needs sshpass)")
	exitName := fs.String("exit-name", "", "label for the EU exit, e.g. eu-aeza-de")
	exitCountry := fs.String("exit-country", "", "EU exit country, e.g. DE")
	wan := fs.String("wan", "", "EU WAN interface for NAT (empty = auto-detect)")
	wgPort := fs.Int("wg-port", 51820, "EU WireGuard listen port")
	iface := fs.String("iface", "wg-cascade", "WireGuard interface name")
	ruIP := fs.String("ru-ip", "10.66.0.2", "RU tunnel IP")
	euIP := fs.String("eu-ip", "10.66.0.1", "EU tunnel IP")
	timeout := fs.Duration("timeout", 30*time.Second, "per-site healthcheck timeout")
	skipCheck := fs.Bool("no-check", false, "skip the site healthcheck")
	_ = fs.Parse(args)

	// Interactive: no EU host on a terminal => ask for everything.
	if *euHost == "" && isInteractive() {
		cascadeUpWizard(euHost, euUser, euPort, euKey, euPass, exitName, exitCountry)
	}
	if *euHost == "" {
		return fmt.Errorf("--eu-host is required (or run `vlr cascade up` on a terminal)")
	}
	if *euKey == "" && *euPass == "" {
		return fmt.Errorf("provide --eu-key or --eu-pass for EU access")
	}
	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}

	// 1. RU WireGuard key — reuse if already stored, else generate.
	var ruPub string
	if c.Cascade.PrivateKey != "" {
		if ruPub, err = wireguard.PublicFromPrivate(c.Cascade.PrivateKey); err != nil {
			return err
		}
	} else {
		kp, err := wireguard.GenerateKeyPair()
		if err != nil {
			return err
		}
		c.Cascade.PrivateKey = kp.PrivateKey
		ruPub = kp.PublicKey
	}

	// Password auth needs sshpass — install it automatically on this RU node
	// instead of failing with an instruction.
	installedSshpass := false
	if *euPass != "" {
		var serr error
		if installedSshpass, serr = ensureSshpass(context.Background()); serr != nil {
			return serr
		}
	}

	// 2. Provision the EU exit over SSH; capture its public key.
	label := *exitName
	if *exitCountry != "" {
		label = strings.TrimSpace(*exitName + " " + *exitCountry)
	}
	if label != "" {
		fmt.Printf("==> EU-выход «%s»\n", label)
	}
	fmt.Printf("==> провижу EU-выход %s (forward-only WireGuard)\n", *euHost)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	euPub, err := cascade.ProvisionExit(ctx,
		cascade.SSHOpts{Host: *euHost, Port: *euPort, User: *euUser, KeyPath: *euKey, Password: *euPass},
		cascade.ExitProvisionParams{
			Iface: *iface, EUAddress: *euIP + "/24", WGPort: *wgPort, WAN: *wan,
			RUPublicKey: ruPub, RUTunnelIP: *ruIP,
		})
	if err != nil {
		return err
	}
	fmt.Printf("    EU pubkey: %s\n", euPub)

	// 3. Fill the RU cascade config and persist it.
	c.Cascade.Enabled = true
	c.Cascade.Interface = *iface
	c.Cascade.Address = *ruIP + "/32"
	if c.Cascade.MTU == 0 {
		c.Cascade.MTU = 1420
	}
	c.Cascade.ExitPublicKey = euPub
	c.Cascade.ExitEndpoint = fmt.Sprintf("%s:%d", *euHost, *wgPort)
	c.Cascade.ExitAllowedIP = "0.0.0.0/0, ::/0"
	c.Cascade.ExitTunnelIP = *euIP
	c.Cascade.ExitName = *exitName
	c.Cascade.ExitCountry = *exitCountry
	if c.Cascade.Keepalive == 0 {
		c.Cascade.Keepalive = 25
	}
	savePath := *cfgPath
	if savePath == "" {
		savePath = config.DefaultPath()
	}
	if err := config.Save(savePath, c); err != nil {
		return err
	}

	// 4. Write the RU wg-quick config and bring the interface up. Ensure the
	// local WireGuard tools and /etc/wireguard exist first (the RU node may not
	// have them — we only installed WireGuard on the EU box so far).
	installedWG, werr := ensureWireguard(context.Background())
	if werr != nil {
		return werr
	}
	ruConf, err := wireguard.RenderEntry(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/wireguard", 0o700); err != nil {
		return fmt.Errorf("mkdir /etc/wireguard: %w", err)
	}
	wgPath := "/etc/wireguard/" + *iface + ".conf"
	if err := os.WriteFile(wgPath, []byte(ruConf), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", wgPath, err)
	}
	fmt.Printf("==> поднимаю туннель RU (%s)\n", *iface)
	_ = exec.CommandContext(ctx, "wg-quick", "down", *iface).Run() // ignore if not up
	if out, err := exec.CommandContext(ctx, "wg-quick", "up", *iface).CombinedOutput(); err != nil {
		return fmt.Errorf("wg-quick up %s: %w\n%s", *iface, err, out)
	}

	// Record what we just created so `vlr uninstall` can reverse it. The EU exit
	// stores host/user/port/key for remote teardown — never the password.
	lp := ledger.DefaultPath(filepath.Dir(savePath))
	if installedSshpass {
		_ = ledger.Record(lp, ledger.KindPackage, "sshpass", nil)
	}
	if installedWG {
		_ = ledger.Record(lp, ledger.KindPackage, "wireguard", nil)
	}
	_ = ledger.Record(lp, ledger.KindFile, wgPath, nil)
	_ = ledger.Record(lp, ledger.KindWGIface, *iface, nil)
	_ = ledger.Record(lp, ledger.KindEUExit, *euHost, map[string]string{
		"user": *euUser, "port": fmt.Sprintf("%d", *euPort),
		"iface": *iface, "key_path": *euKey,
	})

	// 5. Confirm handshake, then healthcheck through the cascade.
	time.Sleep(2 * time.Second)
	up, _ := (cascade.WGMonitor{Interface: *iface}).Healthy(ctx)
	if !up {
		fmt.Println("⚠ нет свежего WireGuard-handshake — проверь, что EU слушает порт и доступен")
	} else {
		fmt.Println("✓ WireGuard handshake есть")
	}
	if *skipCheck {
		return nil
	}
	fmt.Println("\n==> проверка через каскад:")
	results := cascade.Healthcheck(ctx, *iface, nil, *timeout)
	fmt.Print(cascade.FormatResults(results))
	fail := 0
	for _, r := range results {
		if !r.OK {
			fail++
		}
	}
	if fail > 0 {
		return fmt.Errorf("%d/%d сайтов недоступны через каскад", fail, len(results))
	}
	fmt.Println("\n✓ каскад RU→EU работает")
	return nil
}

// ensurePackage makes sure checkBin is available, installing it via the node's
// package manager if missing. pkgByMgr maps a manager binary to the package name
// to install (names differ across distros); defaultPkg is the fallback. Returns
// whether it installed anything (so uninstall can remove it). Root, no sudo.
func ensurePackage(ctx context.Context, checkBin string, pkgByMgr map[string]string, defaultPkg string) (installed bool, err error) {
	if _, e := exec.LookPath(checkBin); e == nil {
		return false, nil
	}
	managers := []struct {
		bin       string
		insFlags  []string
		updateCmd []string
	}{
		{"apt-get", []string{"install", "-y"}, []string{"update", "-qq"}},
		{"apk", []string{"add"}, nil},
		{"dnf", []string{"install", "-y"}, nil},
		{"yum", []string{"install", "-y"}, nil},
	}
	for _, m := range managers {
		if _, e := exec.LookPath(m.bin); e != nil {
			continue
		}
		pkg := defaultPkg
		if p, ok := pkgByMgr[m.bin]; ok {
			pkg = p
		}
		fmt.Printf("==> ставлю %s (%s)\n", pkg, checkBin)
		run := func(args []string) error {
			c := exec.CommandContext(ctx, m.bin, args...)
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			c.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
			return c.Run()
		}
		ins := append(append([]string{}, m.insFlags...), pkg)
		if run(ins) != nil && m.updateCmd != nil {
			_ = run(m.updateCmd) // stale lists -> refresh and retry once
			_ = run(ins)
		}
		if _, e := exec.LookPath(checkBin); e == nil {
			return true, nil
		}
	}
	return false, fmt.Errorf("не смог поставить %s — поставь вручную или используй другой способ", defaultPkg)
}

// ensureSshpass installs sshpass for password SSH auth.
func ensureSshpass(ctx context.Context) (bool, error) {
	return ensurePackage(ctx, "sshpass", nil, "sshpass")
}

// ensureWireguard installs wireguard-tools (wg, wg-quick) on the local RU node.
func ensureWireguard(ctx context.Context) (bool, error) {
	return ensurePackage(ctx, "wg-quick",
		map[string]string{"apt-get": "wireguard"}, "wireguard-tools")
}

// cascadeUpWizard interactively fills the EU exit parameters.
func cascadeUpWizard(euHost, euUser *string, euPort *int, euKey, euPass, exitName, exitCountry *string) {
	fmt.Print(`
========================================
   vlr — поднять каскад RU→EU
========================================
EU-выход будет настроен автоматически по SSH (forward-only WireGuard).

`)
	for *euHost == "" {
		*euHost = ask("IP EU-выхода", "")
	}
	*euUser = ask("SSH-пользователь", "root")
	if p := ask("SSH-порт", "22"); p != "" {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil && n > 0 {
			*euPort = n
		}
	}

	switch ask("Доступ: 1) по ключу  2) по паролю", "1") {
	case "2":
		*euPass = askSecret("SSH-пароль (ввод скрыт)")
	default:
		*euKey = ask("Путь к приватному ключу", os.Getenv("HOME")+"/.ssh/id_ed25519")
	}

	*exitName = ask("Имя EU-выхода (метка, напр. eu-aeza-de)", *exitName)
	*exitCountry = ask("Страна EU-выхода (напр. DE, NL, FI)", *exitCountry)
	fmt.Println()
}

// cmdCascadeCheck re-runs the site healthcheck through an existing cascade.
func cmdCascadeCheck(args []string) error {
	fs := newFlagSet("cascade check")
	cfgPath := fs.String("config", "", "config path")
	sitesCSV := fs.String("sites", "", "comma-separated sites (default: built-in list)")
	timeout := fs.Duration("timeout", 30*time.Second, "per-site timeout")
	_ = fs.Parse(args)

	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	var sites []string
	if *sitesCSV != "" {
		for s := range strings.SplitSeq(*sitesCSV, ",") {
			if s = strings.TrimSpace(s); s != "" {
				sites = append(sites, s)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results := cascade.Healthcheck(ctx, c.Cascade.Interface, sites, *timeout)
	fmt.Print(cascade.FormatResults(results))
	for _, r := range results {
		if !r.OK {
			return fmt.Errorf("some sites unreachable through the cascade")
		}
	}
	return nil
}
