// Command vlr is the global utility for a VLESS+Reality cascade VPN node.
//
//	vlr init        provision this node (standalone | child | main)
//	vlr keys        generate Reality / WireGuard key material
//	vlr cascade     generate RU<->EU WireGuard configs / test the hop
//	vlr user        add | rm | list | link (base64 subscription)
//	vlr node        register | list child nodes (main role)
//	vlr render      print the Xray config for this node
//	vlr serve       run the daemon for this node's role
//	vlr status      show node status
//	vlr version
//
// It is one static binary: the same command is the CLI, the node daemon and the
// main-server API. Role comes from the config file (default /etc/vlr/config.json).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/k1dory/vlr/internal/cascade"
	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/daemon"
	"github.com/k1dory/vlr/internal/reality"
	"github.com/k1dory/vlr/internal/store"
	"github.com/k1dory/vlr/internal/subscription"
	"github.com/k1dory/vlr/internal/util"
	"github.com/k1dory/vlr/internal/wireguard"
	"github.com/k1dory/vlr/internal/xray"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "keys":
		err = cmdKeys(args)
	case "cascade":
		err = cmdCascade(args)
	case "user":
		err = cmdUser(args)
	case "node":
		err = cmdNode(args)
	case "split":
		err = cmdSplit(args)
	case "render":
		err = cmdRender(args)
	case "serve":
		err = cmdServe(args)
	case "status":
		err = cmdStatus(args)
	case "version", "-v", "--version":
		fmt.Printf("vlr %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`vlr — VLESS+Reality cascade VPN node utility

USAGE
  vlr <command> [flags]

COMMANDS
  init        guided setup wizard (run with no flags) or --role standalone|child|main
  keys        generate Reality / WireGuard keys (--type reality|wireguard)
  cascade     gen|exit|test the RU->EU WireGuard hop
  user        add|rm|list|link  (--email, --telegram-id, --profile mobile|desktop)
  split       add|rm|list  RU-direct domains (split-tunnel: egress from RU, not EU)
  node        register|list (main role)
  render      print the Xray config
  serve       run the node daemon for this node's role
  status      show node status
  version     print version

Run "vlr init" on a terminal for the interactive mode menu (1/2/3).
Use "vlr <command> -h" for command flags.
`)
}

// --- helpers ---------------------------------------------------------------

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func loadCfg(path string) (*config.Config, error) {
	if path == "" {
		path = config.DefaultPath()
	}
	return config.Load(path)
}

func dataDir(c *config.Config) string {
	if c.DataDir != "" {
		return c.DataDir
	}
	return filepath.Join(filepath.Dir(config.DefaultPath()), "data")
}

// --- init ------------------------------------------------------------------

func cmdInit(args []string) error {
	fs := newFlagSet("init")
	role := fs.String("role", "standalone", "node role: standalone|child|main")
	nodeID := fs.String("node-id", "", "stable node id, e.g. ru-yc-msk-01")
	host := fs.String("host", "", "public address clients dial; auto-detected if empty")
	port := fs.Int("port", 443, "entry port")
	sni := fs.String("sni", "", "Reality SNI (default: picked from recommended)")
	region := fs.String("region", "", "free-form region label")
	mainURL := fs.String("main-url", "", "child: main server base URL, e.g. https://main/v1")
	token := fs.String("token", "", "child: node token for heartbeat auth")
	pullBearer := fs.String("pull-bearer", "", "child: bearer the main must present to pull")
	apiListen := fs.String("api-listen", "0.0.0.0:8443", "main: API listen address")
	out := fs.String("config", config.DefaultPath(), "config path to write")
	_ = fs.Parse(args)

	// No node id on an interactive terminal => run the guided wizard (mode menu).
	if *nodeID == "" && isInteractive() {
		runInitWizard(role, nodeID, host, region, mainURL, apiListen, token, pullBearer)
	}
	if *nodeID == "" {
		return fmt.Errorf("--node-id is required (or run `vlr init` on a terminal for the guided setup)")
	}
	c := &config.Config{
		Role:    config.Role(*role),
		NodeID:  *nodeID,
		Region:  *region,
		DataDir: filepath.Join(filepath.Dir(*out), "data"),
	}

	switch config.Role(*role) {
	case config.RoleMain:
		c.Main = config.MainConfig{
			APIListen: *apiListen, DownAfterMisses: 3,
			PullThreshold: 256 << 20, ReconcileSeconds: 600,
		}
	case config.RoleStandalone, config.RoleChild:
		// .env: OWN_DOMAIN (attached domain) and DOMAIN_FOR_TLS (Fake-TLS SNIs).
		env := util.LoadDotEnv(util.DotEnvCandidates()...)
		ownDomain := env["OWN_DOMAIN"]

		if *host == "" && ownDomain != "" {
			*host = ownDomain // attached domain is the public address clients dial
			fmt.Printf("host из OWN_DOMAIN: %s\n", ownDomain)
		}
		if *host == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			ip, err := util.DetectPublicIP(ctx)
			cancel()
			if err != nil {
				return fmt.Errorf("auto-detect host failed, pass --host explicitly: %w", err)
			}
			*host = ip
			fmt.Printf("определён публичный host: %s\n", ip)
		}
		kp, err := reality.GenerateKeyPair()
		if err != nil {
			return err
		}
		sids, err := reality.ShortIDSet(3, 8)
		if err != nil {
			return err
		}
		// SNI priority: --sni flag > OWN_DOMAIN > DOMAIN_FOR_TLS list > built-in.
		chosenSNI := *sni
		if chosenSNI == "" && ownDomain != "" {
			chosenSNI = ownDomain // own domain = zero SNI<->IP mismatch (best)
		}
		if chosenSNI == "" {
			if list := util.SplitList(env["DOMAIN_FOR_TLS"]); len(list) > 0 {
				chosenSNI = list[reality.RandIndex(len(list))]
				fmt.Printf("SNI из .env DOMAIN_FOR_TLS: %s\n", chosenSNI)
			}
		}
		if chosenSNI == "" {
			if chosenSNI, err = reality.PickSNI(); err != nil {
				return err
			}
		}
		if err := reality.ValidateSNI(chosenSNI); err != nil {
			return err
		}
		c.Entry = config.EntryConfig{
			Host: *host, Port: *port, SNI: chosenSNI, Dest: chosenSNI + ":443",
			PrivateKey: kp.PrivateKey, PublicKey: kp.PublicKey,
			ShortIDs: sids, Fingerprint: reality.DefaultFingerprint,
		}
		// Split-tunnel: domains that egress directly from RU (no cascade). Seed
		// from .env SPLIT_RU_DOMAINS plus our own host/domain so management and
		// "our system" domains never loop through EU. Edit later: `vlr split`.
		seed := util.SplitList(env["SPLIT_RU_DOMAINS"])
		seed = append(seed, *host)
		if ownDomain != "" {
			seed = append(seed, ownDomain)
		}
		c.Split = config.SplitConfig{RUDirect: dedupNonEmpty(seed)}
		if config.Role(*role) == config.RoleChild {
			c.Child = config.ChildConfig{
				MainURL: *mainURL, Token: *token, PullBearer: *pullBearer,
				HeartbeatSeconds: 20, PullListen: "127.0.0.1:9777",
			}
		}
	default:
		return fmt.Errorf("unknown role %q", *role)
	}

	if err := config.Save(*out, c); err != nil {
		return err
	}
	fmt.Printf("\n✓ конфиг записан: %s  (режим=%s, узел=%s)\n", *out, *role, *nodeID)
	if c.Entry.PublicKey != "" {
		fmt.Printf("  публичный адрес:  %s:%d\n", c.Entry.Host, c.Entry.Port)
		fmt.Printf("  reality pubkey:   %s\n", c.Entry.PublicKey)
		fmt.Printf("  reality SNI:      %s\n", c.Entry.SNI)
		fmt.Printf("  fingerprint:      %s\n", c.Entry.Fingerprint)
		fmt.Println("\nдальше:")
		fmt.Println("  vlr cascade up --eu-host <IP> --eu-user root --eu-key ~/.ssh/id_ed25519   # каскад RU→EU одной командой")
		fmt.Println("  vlr user add --email you@example.com --telegram-id <ID>")
		fmt.Println("  vlr render > /usr/local/etc/xray/config.json && systemctl restart xray")
	}
	if c.Role == config.RoleMain {
		fmt.Println("\nдальше:")
		fmt.Println("  vlr node register --node-id <child> --pull-url https://child:9777/v1/pull --bearer <token>")
		fmt.Println("  systemctl enable --now vlr")
	}
	return nil
}

// --- keys ------------------------------------------------------------------

func cmdKeys(args []string) error {
	fs := newFlagSet("keys")
	kind := fs.String("type", "reality", "reality|wireguard")
	_ = fs.Parse(args)
	switch *kind {
	case "reality":
		kp, err := reality.GenerateKeyPair()
		if err != nil {
			return err
		}
		sid, _ := reality.NewShortID(8)
		fmt.Printf("PrivateKey: %s\nPublicKey:  %s\nShortID:    %s\n", kp.PrivateKey, kp.PublicKey, sid)
	case "wireguard":
		kp, err := wireguard.GenerateKeyPair()
		if err != nil {
			return err
		}
		fmt.Printf("PrivateKey: %s\nPublicKey:  %s\n", kp.PrivateKey, kp.PublicKey)
	default:
		return fmt.Errorf("unknown key type %q", *kind)
	}
	return nil
}

// --- cascade ---------------------------------------------------------------

func cmdCascade(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vlr cascade up|check|gen|exit|test")
	}
	sub, rest := args[0], args[1:]
	if sub == "up" {
		return cmdCascadeUp(rest)
	}
	if sub == "check" {
		return cmdCascadeCheck(rest)
	}
	fs := newFlagSet("cascade")
	cfgPath := fs.String("config", "", "config path")
	switch sub {
	case "gen":
		_ = fs.Parse(rest)
		c, err := loadCfg(*cfgPath)
		if err != nil {
			return err
		}
		conf, err := wireguard.RenderEntry(c)
		if err != nil {
			return err
		}
		fmt.Print(conf)
		return nil
	case "exit":
		listen := fs.Int("listen", 51820, "EU listen port")
		addr := fs.String("addr", "10.66.0.1/24", "EU tunnel address")
		entryPub := fs.String("entry-pubkey", "", "RU entry WG public key")
		entryIP := fs.String("entry-ip", "10.66.0.2/32", "RU tunnel IP")
		wan := fs.String("wan", "eth0", "EU WAN interface for NAT")
		nodeID := fs.String("node-id", "ru-entry", "entry node id (label)")
		_ = fs.Parse(rest)
		kp, err := wireguard.GenerateKeyPair()
		if err != nil {
			return err
		}
		conf, err := wireguard.RenderExit(wireguard.ExitParams{
			NodeID: *nodeID, Interface: "wg-cascade", ListenPort: *listen,
			Address: *addr, PrivateKey: kp.PrivateKey,
			EntryPublicKey: *entryPub, EntryTunnelIP: *entryIP, WANInterface: *wan,
		})
		if err != nil {
			return err
		}
		fmt.Printf("# EU exit public key (put into RU config exit_public_key): %s\n", kp.PublicKey)
		fmt.Print(conf)
		return nil
	case "test":
		_ = fs.Parse(rest)
		c, err := loadCfg(*cfgPath)
		if err != nil {
			return err
		}
		mon := cascade.WGMonitor{Interface: c.Cascade.Interface}
		up, err := mon.Healthy(context.Background())
		if err != nil {
			return fmt.Errorf("cascade test: %w", err)
		}
		if !up {
			return fmt.Errorf("cascade DOWN: no recent WireGuard handshake on %s", c.Cascade.Interface)
		}
		fmt.Println("cascade UP: recent handshake on", c.Cascade.Interface)
		return nil
	default:
		return fmt.Errorf("unknown cascade subcommand %q", sub)
	}
}

// --- user ------------------------------------------------------------------

func cmdUser(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vlr user add|rm|list|link")
	}
	sub, rest := args[0], args[1:]
	fs := newFlagSet("user")
	cfgPath := fs.String("config", "", "config path")
	email := fs.String("email", "", "user email / label")
	profile := fs.String("profile", "mobile", "mobile|desktop (desktop skips Vision)")
	tgID := fs.Int64("telegram-id", 0, "owner's Telegram user id")
	_ = fs.Parse(rest)

	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(dataDir(c))
	if err != nil {
		return err
	}

	switch sub {
	case "add":
		if *email == "" {
			return fmt.Errorf("--email is required")
		}
		uuid, err := util.NewUUID()
		if err != nil {
			return err
		}
		sid := c.Entry.ShortIDs[0]
		if len(c.Entry.ShortIDs) > 1 {
			// spread users across short ids round-robin by count
			sid = c.Entry.ShortIDs[len(st.Users())%len(c.Entry.ShortIDs)]
		}
		u := store.User{UUID: uuid, Email: *email, TelegramID: *tgID, ShortID: sid, Profile: *profile}
		if err := st.AddUser(u); err != nil {
			return err
		}
		fmt.Println(subscription.Link(c.Entry, u))
		return nil
	case "rm":
		if *email == "" {
			return fmt.Errorf("--email is required")
		}
		if err := st.RemoveUser(*email); err != nil {
			return err
		}
		fmt.Println("removed", *email)
		return nil
	case "list":
		for _, u := range st.Users() {
			fmt.Printf("%-30s %s tg=%d profile=%s rx=%d tx=%d\n", u.Email, u.UUID, u.TelegramID, u.Profile, u.RxBytes, u.TxBytes)
		}
		return nil
	case "link":
		if *email == "" {
			return fmt.Errorf("--email is required")
		}
		for _, u := range st.Users() {
			if u.Email == *email {
				fmt.Println("# share link:")
				fmt.Println(subscription.Link(c.Entry, u))
				fmt.Println("# base64 subscription:")
				fmt.Println(subscription.Stream(c.Entry, []store.User{u}))
				return nil
			}
		}
		return fmt.Errorf("user %q not found", *email)
	default:
		return fmt.Errorf("unknown user subcommand %q", sub)
	}
}

// --- node (main role) ------------------------------------------------------

func cmdNode(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vlr node register|list")
	}
	sub, rest := args[0], args[1:]
	fs := newFlagSet("node")
	cfgPath := fs.String("config", "", "config path")
	nodeID := fs.String("node-id", "", "child node id")
	pullURL := fs.String("pull-url", "", "child pull URL, e.g. https://child:9777/v1/pull")
	bearer := fs.String("bearer", "", "pull bearer token the child requires")
	_ = fs.Parse(rest)

	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	regPath := filepath.Join(dataDir(c), "nodes.json")
	regs := loadRegs(regPath)

	switch sub {
	case "register":
		if *nodeID == "" || *pullURL == "" {
			return fmt.Errorf("--node-id and --pull-url are required")
		}
		regs[*nodeID] = daemon.NodeReg{NodeID: *nodeID, PullURL: *pullURL, PullBearer: *bearer}
		if err := saveRegs(regPath, regs); err != nil {
			return err
		}
		fmt.Println("registered", *nodeID)
		return nil
	case "list":
		for _, r := range regs {
			fmt.Printf("%-20s %s\n", r.NodeID, r.PullURL)
		}
		return nil
	default:
		return fmt.Errorf("unknown node subcommand %q", sub)
	}
}

func loadRegs(path string) map[string]daemon.NodeReg {
	out := map[string]daemon.NodeReg{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var list []daemon.NodeReg
	if json.Unmarshal(b, &list) == nil {
		for _, r := range list {
			out[r.NodeID] = r
		}
	}
	return out
}

func saveRegs(path string, regs map[string]daemon.NodeReg) error {
	list := make([]daemon.NodeReg, 0, len(regs))
	for _, r := range regs {
		list = append(list, r)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// --- render ----------------------------------------------------------------

func cmdRender(args []string) error {
	fs := newFlagSet("render")
	cfgPath := fs.String("config", "", "config path")
	_ = fs.Parse(args)
	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(dataDir(c))
	if err != nil {
		return err
	}
	b, err := xray.Render(c, st.Users())
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// --- serve -----------------------------------------------------------------

func cmdServe(args []string) error {
	fs := newFlagSet("serve")
	cfgPath := fs.String("config", "", "config path")
	_ = fs.Parse(args)
	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	log := logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch c.Role {
	case config.RoleMain:
		regs := loadRegs(filepath.Join(dataDir(c), "nodes.json"))
		list := make([]daemon.NodeReg, 0, len(regs))
		for _, r := range regs {
			list = append(list, r)
		}
		return daemon.NewMainServer(c, log, list).Run(ctx)
	case config.RoleChild:
		st, err := store.Open(dataDir(c))
		if err != nil {
			return err
		}
		mon := pickMonitor(c)
		return daemon.NewChild(c, st, log, cascade.NoopStats{}, mon).Run(ctx)
	case config.RoleStandalone:
		st, err := store.Open(dataDir(c))
		if err != nil {
			return err
		}
		mon := pickMonitor(c)
		return daemon.NewStandalone(c, st, log, cascade.NoopStats{}, mon).Run(ctx)
	default:
		return fmt.Errorf("unknown role %q", c.Role)
	}
}

func pickMonitor(c *config.Config) daemon.CascadeMonitor {
	if c.Cascade.Enabled && c.Cascade.Interface != "" {
		return cascade.WGMonitor{Interface: c.Cascade.Interface}
	}
	return cascade.NoopMonitor{}
}

// --- status ----------------------------------------------------------------

func cmdStatus(args []string) error {
	fs := newFlagSet("status")
	cfgPath := fs.String("config", "", "config path")
	_ = fs.Parse(args)
	c, err := loadCfg(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("node:   %s\nrole:   %s\nregion: %s\n", c.NodeID, c.Role, c.Region)
	if c.Entry.Host != "" {
		fmt.Printf("entry:  %s:%d sni=%s fp=%s\n", c.Entry.Host, c.Entry.Port, c.Entry.SNI, c.Entry.Fingerprint)
	}
	if c.Cascade.Enabled {
		up, _ := pickMonitor(c).Healthy(context.Background())
		fmt.Printf("cascade: %s -> %s  up=%v\n", c.Cascade.Address, c.Cascade.ExitEndpoint, up)
	}
	if c.Role != config.RoleMain {
		st, err := store.Open(dataDir(c))
		if err == nil {
			fmt.Printf("users:  %d  config_version=%d  total_bytes=%d\n", len(st.Users()), st.ConfigVersion(), st.TotalBytes())
		}
	}
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return fs
}
