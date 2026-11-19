package cascade

import (
	"context"
	"strings"
	"testing"
)

// Real `ip rule show` of ru-yc-msk-02 after the kill-switch was added by hand:
// the same priority as the tunnel rule, listed (= evaluated) after it.
const rulesWithKillSwitch = `0:	from all lookup local
32764:	from all oif wg-cascade lookup 51820
32765:	from all fwmark 0xca6c lookup 51820
32765:	from all fwmark 0xca6c blackhole
32766:	from all lookup main
32767:	from all lookup default
`

func TestParseKillSwitch(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want KillSwitchState
	}{
		{"absent", rulesOK, KillSwitchState{}},
		{"behind tunnel rule, tied priority", rulesWithKillSwitch, KillSwitchState{Present: true}},
		{"tunnel flushed: kill-switch alone is fine", rulesFlushed + "32765: from all fwmark 0xca6c blackhole\n", KillSwitchState{Present: true}},
		{"shadowing", "32700: from all fwmark 0xca6c blackhole\n32765: from all fwmark 0xca6c lookup 51820\n", KillSwitchState{Present: true, Shadowing: true}},
		{"masked + decimal", "32764: from all fwmark 51820 table 51820\n32765: from all fwmark 0xca6c/0xffffffff blackhole\n", KillSwitchState{Present: true}},
		{"other mark", "32765: from all fwmark 0x1 blackhole\n", KillSwitchState{}},
		{"extra selector is not ours", "32765: from 10.0.0.0/8 fwmark 0xca6c blackhole\n", KillSwitchState{}},
		{"unreachable is not blackhole", "32765: from all fwmark 0xca6c unreachable\n", KillSwitchState{}},
	}
	for _, c := range cases {
		if got := ParseKillSwitch(c.out, 51820); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestEnsureKillSwitchIdempotent(t *testing.T) {
	f := &fakeIP{rules: rulesOK}
	withFake(t, f, true)
	added, err := EnsureKillSwitch(context.Background(), 51820)
	if err != nil || !added {
		t.Fatalf("first pass: added=%v err=%v", added, err)
	}
	if ks := ParseKillSwitch(f.rules, 51820); !ks.Present || ks.Shadowing {
		t.Fatalf("state after add: %+v\n%s", ks, f.rules)
	}
	added, err = EnsureKillSwitch(context.Background(), 51820)
	if err != nil || added || strings.Count(f.rules, "blackhole") != 1 {
		t.Fatalf("second pass: added=%v err=%v\n%s", added, err, f.rules)
	}
}

// The tunnel's PostDown/guard flow: the tunnel rule vanishes and comes back
// (kernel puts it first); the kill-switch must stay and stay behind it.
func TestKillSwitchSurvivesTunnelRuleCycle(t *testing.T) {
	f := &fakeIP{rules: rulesFlushed, routes: "default dev wg-cascade\n"}
	withFake(t, f, true)
	if _, err := EnsureKillSwitch(context.Background(), 51820); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRules(context.Background(), "wg-cascade", "", 51820); err != nil {
		t.Fatal(err)
	}
	if ks := ParseKillSwitch(f.rules, 51820); !ks.Present || ks.Shadowing {
		t.Fatalf("after tunnel rule re-add: %+v\n%s", ks, f.rules)
	}
}

func TestEnsureKillSwitchRemovesShadowing(t *testing.T) {
	f := &fakeIP{rules: "32765: from all fwmark 0xca6c blackhole\n32766: from all fwmark 0xca6c lookup 51820\n"}
	withFake(t, f, true)
	added, err := EnsureKillSwitch(context.Background(), 51820)
	if err == nil || added {
		t.Fatalf("shadowing must be reported: added=%v err=%v", added, err)
	}
	if strings.Contains(f.rules, "blackhole") {
		t.Fatalf("shadowing kill-switch left in place:\n%s", f.rules)
	}
}

func TestRemoveKillSwitch(t *testing.T) {
	f := &fakeIP{rules: rulesWithKillSwitch}
	withFake(t, f, true)
	RemoveKillSwitch(context.Background(), 51820)
	if strings.Contains(f.rules, "blackhole") || !strings.Contains(f.rules, "fwmark 0xca6c lookup 51820") {
		t.Fatalf("remove touched the wrong rules:\n%s", f.rules)
	}
}

func TestKillSwitchDropInBody(t *testing.T) {
	b := KillSwitchDropInBody(51820)
	for _, want := range []string{
		"[Service]",
		"ExecStartPre=-+/bin/sh -c '",
		`grep -q "^32765:.*fwmark 0xca6c blackhole"`,
		"ip -4 rule add pref 32765 fwmark 51820 blackhole'",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("drop-in lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "%") {
		t.Errorf("unexpanded format verb / systemd specifier in drop-in:\n%s", b)
	}
}
