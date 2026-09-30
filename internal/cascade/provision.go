package cascade

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/k1dory/vlr/internal/config"
)

// This file automates the cascade the way Genomed-mtproto's `mtg kaskad` did:
// from the RU entry node, one command provisions the EU exit over SSH and brings
// up the AmneziaWG tunnel. The EU side is "forward-only" — it generates its own
// private key (which never leaves the box), only accepts the RU peer's tunnel IP,
// and only masquerades traffic out (no shell, no general routing). WireGuard
// replaces mtg's autossh SOCKS tunnel, so there is no persistent SSH at all:
// SSH is used once, for provisioning.

// SSHOpts describes how to reach the EU box for provisioning.
type SSHOpts struct {
	Host     string
	Port     int
	User     string
	KeyPath  string // key auth (preferred)
	Password string // password auth (needs sshpass installed)
}

// ExitProvisionParams parameterises the remote EU bootstrap script.
type ExitProvisionParams struct {
	AWG         *config.AWGConfig
	Autostart   bool
	Iface       string // wg-cascade
	EUAddress   string // EU tunnel addr with mask, e.g. 10.66.0.1/24
	WGPort      int    // EU WireGuard listen port (RU endpoint points here)
	WAN         string // EU WAN nic for NAT; "" => auto-detect on the box
	RUPublicKey string // RU node WG public key (the only allowed peer)
	RUTunnelIP  string // RU tunnel IP, e.g. 10.66.0.2
}

// BuildExitScript renders the idempotent bash script run on the EU exit. It
// installs AmneziaWG if missing, generates EU keys locally, writes a forward-only
// exit config and brings it up, then prints "VLR_EU_PUBKEY=<pub>" for the caller.
// Pure function — unit-tested, no I/O.
func BuildExitScript(p ExitProvisionParams) string {
	autostart := "disable"
	if p.Autostart {
		autostart = "enable"
	}
	awg := ""
	if p.AWG != nil {
		awg = p.AWG.Render()
	}
	r := strings.NewReplacer(
		"{{INSTALL}}", AWGInstallScript,
		"{{AWG}}", awg,
		"{{AUTOSTART}}", autostart,
		"{{IFACE}}", p.Iface,
		"{{ADDR}}", p.EUAddress,
		"{{PORT}}", fmt.Sprintf("%d", p.WGPort),
		"{{WAN}}", p.WAN,
		"{{RU_PUB}}", p.RUPublicKey,
		"{{RU_IP}}", p.RUTunnelIP,
	)
	return r.Replace(exitScriptTemplate)
}

const exitScriptTemplate = `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

umask 077
{{INSTALL}}
if ! command -v iptables >/dev/null; then
  apt-get install -y iptables
fi
# Stop the old interface before replacing its config, so PostDown uses old rules.
if [ -f /etc/wireguard/{{IFACE}}.conf ]; then
  systemctl disable --now wg-quick@{{IFACE}}
  wg-quick down {{IFACE}} >/dev/null 2>&1 || true
fi
systemctl stop awg-quick@{{IFACE}} || true
awg-quick down {{IFACE}} >/dev/null 2>&1 || true

WAN="{{WAN}}"
if [ -z "$WAN" ]; then
  WAN="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
fi
[ -n "$WAN" ] || { echo "could not detect WAN interface" >&2; exit 1; }

mkdir -p /etc/amnezia/amneziawg && chmod 700 /etc/amnezia/amneziawg
if [ ! -f /etc/amnezia/amneziawg/{{IFACE}}.key ]; then
  if [ -f /etc/wireguard/{{IFACE}}.key ]; then
    cp /etc/wireguard/{{IFACE}}.key /etc/amnezia/amneziawg/{{IFACE}}.key
  else
    awg genkey > /etc/amnezia/amneziawg/{{IFACE}}.key
  fi
fi
awg pubkey < /etc/amnezia/amneziawg/{{IFACE}}.key > /etc/amnezia/amneziawg/{{IFACE}}.pub
EU_PRIV="$(cat /etc/amnezia/amneziawg/{{IFACE}}.key)"
EU_PUB="$(cat /etc/amnezia/amneziawg/{{IFACE}}.pub)"

sysctl -wq net.ipv4.ip_forward=1
grep -q '^net.ipv4.ip_forward=1' /etc/sysctl.conf || echo 'net.ipv4.ip_forward=1' >> /etc/sysctl.conf

cat > /etc/amnezia/amneziawg/{{IFACE}}.conf <<EOF
# vlr cascade — EU exit (forward-only: NAT out, only the RU peer allowed)
[Interface]
Address = {{ADDR}}
PrivateKey = $EU_PRIV
ListenPort = {{PORT}}
{{AWG}}
PostUp = iptables -I INPUT 1 -p udp --dport {{PORT}} -m comment --comment vlr-%i -j ACCEPT
PostUp = iptables -I FORWARD 1 -i %i -s {{RU_IP}}/32 -o $WAN -j ACCEPT
PostUp = iptables -I FORWARD 1 -i $WAN -o %i -d {{RU_IP}}/32 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
PostUp = iptables -t nat -A POSTROUTING -s {{RU_IP}}/32 -o $WAN -j MASQUERADE
PostDown = iptables -D INPUT -p udp --dport {{PORT}} -m comment --comment vlr-%i -j ACCEPT
PostDown = iptables -D FORWARD -i %i -s {{RU_IP}}/32 -o $WAN -j ACCEPT
PostDown = iptables -D FORWARD -i $WAN -o %i -d {{RU_IP}}/32 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
PostDown = iptables -t nat -D POSTROUTING -s {{RU_IP}}/32 -o $WAN -j MASQUERADE

[Peer]
PublicKey = {{RU_PUB}}
AllowedIPs = {{RU_IP}}/32
EOF
chmod 600 /etc/amnezia/amneziawg/{{IFACE}}.conf

systemctl {{AUTOSTART}} awg-quick@{{IFACE}}
systemctl start awg-quick@{{IFACE}}

echo "VLR_EU_PUBKEY=$EU_PUB"
`

// ProvisionExit runs the EU bootstrap over SSH and returns the EU AmneziaWG
// public key captured from the script output.
func ProvisionExit(ctx context.Context, ssh SSHOpts, p ExitProvisionParams) (euPubKey string, err error) {
	if err := ValidateExitParams(p); err != nil {
		return "", err
	}
	if raw, err := base64.StdEncoding.DecodeString(p.RUPublicKey); err != nil || len(raw) != 32 {
		return "", fmt.Errorf("invalid RU public key")
	}
	if err := p.AWG.Validate(); err != nil {
		return "", err
	}
	script := BuildExitScript(p)
	remoteCmd := "bash -s"
	if ssh.User != "root" {
		remoteCmd = "sudo -n bash -s"
	}
	out, err := runSSH(ctx, ssh, remoteCmd, script)
	if err != nil {
		return "", fmt.Errorf("EU provisioning failed: %w\n%s", err, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VLR_EU_PUBKEY="); ok {
			key := strings.TrimSpace(v)
			if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 32 {
				return "", fmt.Errorf("invalid EU public key")
			}
			return key, nil
		}
	}
	return "", fmt.Errorf("EU public key not found in output:\n%s", out)
}

// TeardownExit reverses ProvisionExit on the EU box: brings the interface down
// (which runs the iptables PostDown), disables the unit and removes the conf +
// keys. Idempotent — tolerates an already-clean box.
func TeardownExit(ctx context.Context, ssh SSHOpts, iface string) (string, error) {
	if iface == "" {
		iface = "wg-cascade"
	}
	if !interfaceName.MatchString(iface) || iface == "." || iface == ".." {
		return "", fmt.Errorf("invalid tunnel interface name")
	}
	script := strings.NewReplacer("{{IFACE}}", iface).Replace(exitTeardownTemplate)
	remoteCmd := "bash -s"
	if ssh.User != "root" {
		remoteCmd = "sudo -n bash -s"
	}
	return runSSH(ctx, ssh, remoteCmd, script)
}

const exitTeardownTemplate = `set -uo pipefail
awg-quick down {{IFACE}} 2>/dev/null || true
systemctl disable --now awg-quick@{{IFACE}} 2>/dev/null || true
rm -f /etc/amnezia/amneziawg/{{IFACE}}.conf /etc/amnezia/amneziawg/{{IFACE}}.key /etc/amnezia/amneziawg/{{IFACE}}.pub
wg-quick down {{IFACE}} 2>/dev/null || true
systemctl disable wg-quick@{{IFACE}} 2>/dev/null || true
rm -f /etc/wireguard/{{IFACE}}.conf /etc/wireguard/{{IFACE}}.key /etc/wireguard/{{IFACE}}.pub
echo "VLR_EU_TEARDOWN_OK"
`

// runSSH executes remoteCmd on the EU box, feeding stdin to it, using key or
// password auth. It shells out to the system ssh (and sshpass for passwords) so
// the vlr binary stays free of an SSH library dependency.
func runSSH(ctx context.Context, o SSHOpts, remoteCmd, stdin string) (string, error) {
	if o.Host == "" || o.User == "" {
		return "", fmt.Errorf("ssh host and user are required")
	}
	port := o.Port
	if port == 0 {
		port = 22
	}
	common := []string{
		"-p", fmt.Sprintf("%d", port),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
	}
	target := o.User + "@" + o.Host

	var name string
	var args []string
	switch {
	case o.KeyPath != "":
		name = "ssh"
		args = append([]string{"-i", o.KeyPath, "-o", "BatchMode=yes"}, common...)
		args = append(args, target, remoteCmd)
	case o.Password != "":
		if _, err := exec.LookPath("sshpass"); err != nil {
			return "", fmt.Errorf("password auth needs sshpass installed (apt-get install -y sshpass), or use --eu-key")
		}
		name = "sshpass"
		args = append([]string{"-p", o.Password, "ssh"}, common...)
		args = append(args, target, remoteCmd)
	default:
		return "", fmt.Errorf("provide --eu-key or --eu-pass for EU access")
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// --- healthcheck -----------------------------------------------------------

// DefaultCheckSites is the reachability list run through the cascade.
var DefaultCheckSites = []string{
	"telegram.org",
	"amazon.com",
	"claude.ai",
	"openai.com",
	"notebooklm.google.com",
	"google.com",
}

// SiteResult is one site reachability probe through the cascade.
type SiteResult struct {
	Host string
	OK   bool
	Code string
	Dur  time.Duration
	Err  string
}

// Healthcheck probes each site THROUGH the cascade by binding curl to the WG
// interface, so a green result proves the RU->EU->internet path works (not just
// the RU node's own connectivity). timeout is per-site.
func Healthcheck(ctx context.Context, iface string, sites []string, timeout time.Duration) []SiteResult {
	return HealthcheckProgress(ctx, iface, sites, timeout, nil)
}

// HealthcheckProgress reports each completed probe without waiting for the rest.
func HealthcheckProgress(ctx context.Context, iface string, sites []string, timeout time.Duration, progress func(SiteResult)) []SiteResult {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if len(sites) == 0 {
		sites = DefaultCheckSites
	}
	results := make([]SiteResult, 0, len(sites))
	for _, host := range sites {
		result := probeSite(ctx, iface, host, timeout)
		results = append(results, result)
		if progress != nil {
			progress(result)
		}
	}
	return results
}

func probeSite(ctx context.Context, iface, host string, timeout time.Duration) SiteResult {
	args := []string{
		"-4", "-sS", "-o", "/dev/null",
		"-w", "%{http_code}",
		"--max-time", fmt.Sprintf("%d", int(timeout.Seconds())),
	}
	if iface != "" {
		args = append(args, "--interface", iface)
	}
	args = append(args, "https://"+host)

	start := time.Now()
	cmd := exec.CommandContext(ctx, "curl", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	dur := time.Since(start)

	code := strings.TrimSpace(out.String())
	res := SiteResult{Host: host, Code: code, Dur: dur}
	if err != nil || code == "" || code == "000" {
		res.OK = false
		if e := strings.TrimSpace(errb.String()); e != "" {
			res.Err = e
		} else {
			res.Err = fmt.Sprintf("Time to request failed (%ds)", int(timeout.Seconds()))
		}
		return res
	}
	res.OK = true // any HTTP response (2xx/3xx/4xx) means the path reached the host
	return res
}

// FormatResults renders the OK/FAIL table the operator sees.
func FormatResults(rs []SiteResult) string {
	var b strings.Builder
	width := 0
	for _, r := range rs {
		if len(r.Host) > width {
			width = len(r.Host)
		}
	}
	for _, r := range rs {
		status := "[OK]"
		if !r.OK {
			status = "[FAIL]"
		}
		fmt.Fprintf(&b, "%-*s %s\n", width+2, r.Host, status)
		if !r.OK {
			fmt.Fprintf(&b, "  logs: %s\n", r.Err)
		}
	}
	return b.String()
}
