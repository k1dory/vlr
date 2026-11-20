package cascade

// Kill-switch for the cascade. Xray marks its EU-bound egress with the cascade
// fwmark, and the tunnel rule (`fwmark <m> lookup <m>`) sends it into a table
// whose only route is the tunnel. When that route disappears — awg-quick
// restart, interface down, a reboot before the tunnel is up — the lookup falls
// through to the main table and marked traffic silently leaves from the RU
// address. The kill-switch is a second rule, evaluated after the tunnel rule,
// that blackholes marked traffic: while the tunnel is up it is never reached;
// while it is down, EU-bound traffic is dropped instead of leaking.
//
// It is a separate rule (not a route in the tunnel table) because the tunnel's
// PostDown deletes `fwmark <m> table <m>`; a blackhole action rule has no table
// and is never matched by that delete. Established TCP flows survive a short
// tunnel restart through retransmits; new connections during the gap fail fast
// and are retried by the client.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// KillSwitchPref is the kill-switch rule priority. The tunnel rule is added
// without a priority, and the kernel then picks (lowest non-local priority - 1),
// so it always lands before this one; main is 32766.
const KillSwitchPref = 32765

// KillSwitchDropIn installs the kill-switch before Xray starts, so marked
// traffic cannot leak in the boot window before the tunnel and the vlr daemon
// are up.
const KillSwitchDropIn = "/etc/systemd/system/xray.service.d/20-vlr-killswitch.conf"

// KillSwitchDropInBody renders the Xray drop-in. "+" runs the hook as root even
// when Xray runs as an unprivileged user; "-" keeps Xray starting if ip fails
// (the vlr daemon re-adds the rule).
func KillSwitchDropInBody(mark int) string {
	return fmt.Sprintf(`# vlr: cascade kill-switch. Marked (EU-bound) Xray egress must never leave via
# the RU default route: when the tunnel table has no route, this rule (placed
# after the tunnel rule) drops it instead. Installed before Xray starts.
[Service]
ExecStartPre=-+/bin/sh -c 'ip -4 rule show | grep -q "^%d:.*fwmark %#x blackhole" || ip -4 rule add pref %d fwmark %d blackhole'
`, KillSwitchPref, mark, KillSwitchPref, mark)
}

// EnsureKillSwitchDropIn writes the Xray drop-in; changed reports whether
// systemd needs a daemon-reload.
func EnsureKillSwitchDropIn(path string, mark int) (bool, error) {
	return writeIfChanged(path, KillSwitchDropInBody(mark))
}

// KillSwitchState is the kill-switch as seen in `ip rule show`.
type KillSwitchState struct {
	Present bool
	// Shadowing: the kill-switch is evaluated before the tunnel rule, so ALL
	// marked traffic is dropped even with the tunnel up.
	Shadowing bool
}

// ParseKillSwitch inspects `ip -N -4 rule show` output. Lines are listed in
// evaluation order, so position (not the priority number, which may tie)
// decides whether the kill-switch shadows the tunnel rule.
func ParseKillSwitch(out string, mark int) KillSwitchState {
	var st KillSwitchState
	table := strconv.Itoa(mark)
	ksAt, tunAt := -1, -1
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && strings.HasSuffix(f[0], ":") {
			f = f[1:]
		}
		if len(f) < 5 || f[0] != "from" || f[1] != "all" || f[2] != "fwmark" || !markIs(f[3], mark) {
			continue
		}
		switch {
		case len(f) == 5 && f[4] == "blackhole":
			if ksAt < 0 {
				ksAt = i
			}
		case len(f) == 6 && (f[4] == "lookup" || f[4] == "table") && f[5] == table:
			if tunAt < 0 {
				tunAt = i
			}
		}
	}
	st.Present = ksAt >= 0
	st.Shadowing = st.Present && tunAt >= 0 && ksAt < tunAt
	return st
}

func markIs(v string, mark int) bool {
	v, _, _ = strings.Cut(v, "/")
	n, err := strconv.ParseUint(v, 0, 32)
	return err == nil && n == uint64(mark)
}

func killSwitchArgs(op string, mark int) []string {
	return []string{"-4", "rule", op, "pref", strconv.Itoa(KillSwitchPref), "fwmark", strconv.Itoa(mark), "blackhole"}
}

// EnsureKillSwitch adds the kill-switch rule if missing. It never touches the
// tunnel's own rules, so it is safe while awg-quick is starting or stopping.
// A kill-switch that shadows the tunnel rule (someone added the tunnel rule
// with a higher priority) is removed and reported: dropping all EU traffic with
// the tunnel up is worse than the leak it guards against.
func EnsureKillSwitch(ctx context.Context, mark int) (added bool, err error) {
	out, err := runIP(ctx, "-N", "-4", "rule", "show")
	if err != nil {
		return false, err
	}
	st := ParseKillSwitch(string(out), mark)
	if st.Shadowing {
		_, derr := runIP(ctx, killSwitchArgs("del", mark)...)
		return false, fmt.Errorf("kill-switch (pref %d) was evaluated before the tunnel rule and blackholed all marked traffic; removed it (del err: %v) — fix the tunnel rule priority", KillSwitchPref, derr)
	}
	if st.Present {
		return false, nil
	}
	if _, err := runIP(ctx, killSwitchArgs("add", mark)...); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveKillSwitch deletes every copy of the kill-switch rule (uninstall).
func RemoveKillSwitch(ctx context.Context, mark int) {
	for i := 0; i < 8; i++ {
		if _, err := runIP(ctx, killSwitchArgs("del", mark)...); err != nil {
			return
		}
	}
}
