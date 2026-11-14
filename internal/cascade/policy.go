package cascade

// Policy routing for the cascade. Xray marks its egress sockets with the
// cascade fwmark; two ip rules send marked traffic (and anything bound to the
// tunnel) into a table of the same number whose default route is the tunnel.
// awg-quick's PostUp installs them, but nothing keeps them there:
// systemd-networkd (ManageForeignRoutingPolicyRules=yes by default) flushes
// "foreign" rules every time it starts — e.g. when unattended-upgrades restarts
// it after a libc update. The tunnel stays up and handshaking, yet all Xray
// traffic silently leaves from the RU address. This file closes that gap:
//   - a networkd drop-in so networkd stops flushing our rules/routes;
//   - EnsureRules / GuardRules to restore them if they vanish anyway;
//   - Egress to prove marked traffic really leaves through the exit.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// NetworkdDropIn keeps systemd-networkd from deleting policy rules and routes
// it did not create.
const NetworkdDropIn = "/etc/systemd/networkd.conf.d/10-vlr-keep-cascade.conf"

const networkdDropInBody = `# vlr: keep the cascade policy routing (fwmark rules, tunnel table) across
# systemd-networkd restarts. Without this, networkd flushes "foreign" rules on
# start and Xray egress silently bypasses the cascade.
[Network]
ManageForeignRoutingPolicyRules=no
ManageForeignRoutes=no
`

// EnsureNetworkdKeepsRules writes the networkd drop-in at path. It is a no-op
// (false, nil) on hosts without systemd or when the file already has the right
// content. It takes effect on networkd's next start — exactly the moment that
// would otherwise flush the rules — so networkd is deliberately not restarted.
func EnsureNetworkdKeepsRules(path string) (bool, error) {
	if _, err := os.Stat("/etc/systemd"); err != nil {
		return false, nil
	}
	return writeIfChanged(path, networkdDropInBody)
}

func writeIfChanged(path, body string) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// RuleState is what the policy routing currently looks like.
type RuleState struct {
	Fwmark bool // fwmark <mark> lookup <mark>
	Oif    bool // oif <iface> lookup <mark>
	Route  bool // default route in table <mark>
}

// OK reports whether marked traffic will be routed into the tunnel.
func (s RuleState) OK() bool { return s.Fwmark && s.Oif && s.Route }

// ParseRules inspects `ip -N -4 rule show` output for the two cascade rules.
// Only the exact general rules count: a rule with extra selectors (e.g. both
// fwmark and oif, a source/destination, a suppressor) does not route Xray's
// marked sockets or the interface-bound probes on its own.
func ParseRules(out, iface string, mark int) RuleState {
	var st RuleState
	table := strconv.Itoa(mark)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && strings.HasSuffix(f[0], ":") {
			f = f[1:] // priority
		}
		if len(f) != 6 || f[0] != "from" || f[1] != "all" || f[4] != "lookup" && f[4] != "table" || f[5] != table {
			continue
		}
		switch f[2] {
		case "fwmark":
			v, _, _ := strings.Cut(f[3], "/")
			if n, err := strconv.ParseUint(v, 0, 32); err == nil && n == uint64(mark) {
				st.Fwmark = true
			}
		case "oif":
			if f[3] == iface {
				st.Oif = true
			}
		}
	}
	return st
}

// ParseTableRoute reports whether `ip -4 route show table <mark>` has a default
// route through iface.
func ParseTableRoute(out, iface string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "default" && ruleArg(f, "dev") == iface {
			return true
		}
	}
	return false
}

func ruleArg(f []string, key string) string {
	for i := 0; i+1 < len(f); i++ {
		if f[i] == key {
			return f[i+1]
		}
	}
	return ""
}

// Hooks for tests.
var (
	runIP = func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "ip", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	ifaceExists = func(name string) bool {
		_, err := os.Stat(filepath.Join("/sys/class/net", name))
		return err == nil
	}
	// unitActive reports whether the tunnel's systemd unit has finished
	// starting (PostUp done) and is not stopping. While awg-quick is mid-up or
	// mid-down the guard must not touch routing: racing PostUp/PostDown is how
	// duplicate rules or a failed tunnel start happen.
	unitActive = func(ctx context.Context, unit string) bool {
		out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "-p", "ActiveState", "--value").Output()
		return err == nil && strings.TrimSpace(string(out)) == "active"
	}
	// confirmDelay: a missing rule must still be missing this much later
	// before the guard repairs it (covers a manual `awg-quick up` outside systemd).
	confirmDelay = 5 * time.Second
)

// ReadRules returns the current policy-routing state for the cascade.
func ReadRules(ctx context.Context, iface string, mark int) (RuleState, error) {
	rules, err := runIP(ctx, "-N", "-4", "rule", "show")
	if err != nil {
		return RuleState{}, err
	}
	st := ParseRules(string(rules), iface, mark)
	routes, err := runIP(ctx, "-N", "-4", "route", "show", "table", strconv.Itoa(mark))
	if err != nil {
		return st, err
	}
	st.Route = ParseTableRoute(string(routes), iface)
	return st, nil
}

// EnsureRules restores whatever part of the cascade policy routing is missing
// and returns what it added. It does nothing unless the tunnel interface exists
// and its unit (if given) is fully active: while the tunnel is down, starting
// or stopping, awg-quick owns the rules.
func EnsureRules(ctx context.Context, iface, unit string, mark int) ([]string, error) {
	ready := func() bool {
		return iface != "" && ifaceExists(iface) && (unit == "" || unitActive(ctx, unit))
	}
	if !ready() {
		return nil, nil
	}
	st, err := ReadRules(ctx, iface, mark)
	if err != nil {
		return nil, err
	}
	m := strconv.Itoa(mark)
	var added []string
	if st.OK() {
		return nil, nil
	}
	if !ready() { // re-check right before writing
		return nil, nil
	}
	if !st.Route {
		if _, err := runIP(ctx, "-4", "route", "replace", "default", "dev", iface, "table", m); err != nil {
			return added, err
		}
		added = append(added, "route default dev "+iface+" table "+m)
	}
	if !st.Fwmark {
		if _, err := runIP(ctx, "-4", "rule", "add", "fwmark", m, "table", m); err != nil {
			return added, err
		}
		added = append(added, "rule fwmark "+m+" table "+m)
	}
	if !st.Oif {
		if _, err := runIP(ctx, "-4", "rule", "add", "oif", iface, "table", m); err != nil {
			return added, err
		}
		added = append(added, "rule oif "+iface+" table "+m)
	}
	return added, nil
}

// GuardRules re-checks the cascade policy routing every interval and restores
// it if something (networkd, an operator, a script) removed it. A gap must
// persist for confirmDelay before it is repaired. It runs until ctx is done.
func GuardRules(ctx context.Context, log *slog.Logger, iface, unit string, mark int, every time.Duration) {
	if every <= 0 {
		every = 30 * time.Second
	}
	check := func() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		st, err := ReadRules(cctx, iface, mark)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Error("cascade policy routing check failed", "iface", iface, "err", err)
			}
			return
		}
		if st.OK() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(confirmDelay):
		}
		cctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		added, err := EnsureRules(cctx, iface, unit, mark)
		if len(added) > 0 {
			log.Warn("cascade policy routing was missing; restored", "iface", iface, "added", added)
		}
		if err != nil && ctx.Err() == nil {
			log.Error("cascade policy routing repair failed", "iface", iface, "err", err)
		}
	}
	check()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
