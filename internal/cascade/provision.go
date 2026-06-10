package cascade

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// This file automates the cascade the way Genomed-mtproto's `mtg kaskad` did:
// from the RU entry node, one command provisions the EU exit over SSH and brings
// up the WireGuard tunnel. The EU side is "forward-only" — it generates its own
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
	Iface       string // wg-cascade
	EUAddress   string // EU tunnel addr with mask, e.g. 10.66.0.1/24
	WGPort      int    // EU WireGuard listen port (RU endpoint points here)
	WAN         string // EU WAN nic for NAT; "" => auto-detect on the box
	RUPublicKey string // RU node WG public key (the only allowed peer)
	RUTunnelIP  string // RU tunnel IP, e.g. 10.66.0.2
}

// BuildExitScript renders the idempotent bash script run on the EU exit. It
// installs WireGuard if missing, generates EU keys locally, writes a forward-only
// exit config and brings it up, then prints "VLR_EU_PUBKEY=<pub>" for the caller.
// Pure function — unit-tested, no I/O.
func BuildExitScript(p ExitProvisionParams) string {
	r := strings.NewReplacer(
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

if ! command -v wg >/dev/null 2>&1; then
  echo ">> installing wireguard"
  (apt-get update -qq && apt-get install -y -qq wireguard iptables) >/dev/null
fi

WAN="{{WAN}}"
if [ -z "$WAN" ]; then
  WAN="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
fi
[ -n "$WAN" ] || { echo "could not detect WAN interface" >&2; exit 1; }

mkdir -p /etc/wireguard && chmod 700 /etc/wireguard
if [ ! -f /etc/wireguard/{{IFACE}}.key ]; then
  umask 077
  wg genkey | tee /etc/wireguard/{{IFACE}}.key | wg pubkey > /etc/wireguard/{{IFACE}}.pub
fi
EU_PRIV="$(cat /etc/wireguard/{{IFACE}}.key)"
EU_PUB="$(cat /etc/wireguard/{{IFACE}}.pub)"

sysctl -wq net.ipv4.ip_forward=1
grep -q '^net.ipv4.ip_forward=1' /etc/sysctl.conf || echo 'net.ipv4.ip_forward=1' >> /etc/sysctl.conf

cat > /etc/wireguard/{{IFACE}}.conf <<EOF
# vlr cascade — EU exit (forward-only: NAT out, only the RU peer allowed)
[Interface]
Address = {{ADDR}}
PrivateKey = $EU_PRIV
ListenPort = {{PORT}}
PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -A FORWARD -o %i -j ACCEPT; iptables -t nat -A POSTROUTING -o $WAN -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -D FORWARD -o %i -j ACCEPT; iptables -t nat -D POSTROUTING -o $WAN -j MASQUERADE

[Peer]
PublicKey = {{RU_PUB}}
AllowedIPs = {{RU_IP}}/32
EOF
chmod 600 /etc/wireguard/{{IFACE}}.conf

systemctl enable wg-quick@{{IFACE}} >/dev/null 2>&1 || true
wg-quick down {{IFACE}} >/dev/null 2>&1 || true
wg-quick up {{IFACE}}

echo "VLR_EU_PUBKEY=$EU_PUB"
`

// ProvisionExit runs the EU bootstrap over SSH and returns the EU WireGuard
// public key captured from the script output.
func ProvisionExit(ctx context.Context, ssh SSHOpts, p ExitProvisionParams) (euPubKey string, err error) {
	script := BuildExitScript(p)
	out, err := runSSH(ctx, ssh, "bash -s", script)
	if err != nil {
		return "", fmt.Errorf("EU provisioning failed: %w\n%s", err, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VLR_EU_PUBKEY="); ok {
			return strings.TrimSpace(v), nil
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
	script := strings.NewReplacer("{{IFACE}}", iface).Replace(exitTeardownTemplate)
	return runSSH(ctx, ssh, "bash -s", script)
}

const exitTeardownTemplate = `set -uo pipefail
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
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if len(sites) == 0 {
		sites = DefaultCheckSites
	}
	results := make([]SiteResult, 0, len(sites))
	for _, host := range sites {
		results = append(results, probeSite(ctx, iface, host, timeout))
	}
	return results
}

func probeSite(ctx context.Context, iface, host string, timeout time.Duration) SiteResult {
	args := []string{
		"-sS", "-o", "/dev/null",
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
