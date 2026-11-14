package cascade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rulesOK = `0:	from all lookup local
32764:	from all oif wg-cascade lookup 51820
32765:	from all fwmark 0xca6c lookup 51820
32766:	from all lookup main
32767:	from all lookup default
`

// What the main node looked like after networkd flushed foreign rules.
const rulesFlushed = `0:	from all lookup local
32766:	from all lookup main
32767:	from all lookup default
`

func TestParseRules(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want RuleState
	}{
		{"both", rulesOK, RuleState{Fwmark: true, Oif: true}},
		{"flushed", rulesFlushed, RuleState{}},
		{"masked fwmark", "100: from all fwmark 0xca6c/0xffffffff lookup 51820\n", RuleState{Fwmark: true}},
		{"decimal + table keyword", "100: from all fwmark 51820 table 51820\n", RuleState{Fwmark: true}},
		{"other table", "100: from all fwmark 0xca6c lookup 100\n101: from all oif wg-cascade lookup main\n", RuleState{}},
		{"other iface", "100: from all oif wg0 lookup 51820\n", RuleState{}},
		{"legacy not fwmark is not ours", "100: not from all fwmark 0xca6c lookup 51820\n", RuleState{}},
		{"combined selectors count as neither", "100: from all fwmark 0xca6c oif wg-cascade lookup 51820\n", RuleState{}},
		{"source-restricted is not general", "100: from 10.0.0.0/8 fwmark 0xca6c lookup 51820\n", RuleState{}},
		{"suppressor is not general", "100: from all fwmark 0xca6c lookup 51820 suppress_prefixlength 0\n", RuleState{}},
	}
	for _, c := range cases {
		if got := ParseRules(c.out, "wg-cascade", 51820); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestParseTableRoute(t *testing.T) {
	if !ParseTableRoute("default dev wg-cascade scope link \n", "wg-cascade") {
		t.Fatal("default route not detected")
	}
	if ParseTableRoute("", "wg-cascade") || ParseTableRoute("default dev eth0 scope link\n", "wg-cascade") {
		t.Fatal("false positive")
	}
}

type fakeIP struct {
	rules, routes string
	calls         []string
	failAdd       bool
}

func (f *fakeIP) run(_ context.Context, args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	switch {
	case cmd == "-N -4 rule show":
		return []byte(f.rules), nil
	case cmd == "-N -4 route show table 51820":
		return []byte(f.routes), nil
	case f.failAdd:
		return nil, errors.New("boom")
	case strings.HasPrefix(cmd, "-4 rule add fwmark"):
		f.rules += "100: from all fwmark 0xca6c lookup 51820\n"
	case strings.HasPrefix(cmd, "-4 rule add oif"):
		f.rules += "101: from all oif wg-cascade lookup 51820\n"
	case strings.HasPrefix(cmd, "-4 route replace"):
		f.routes = "default dev wg-cascade scope link\n"
	}
	return nil, nil
}

func withFake(t *testing.T, f *fakeIP, exists bool) {
	t.Helper()
	oldRun, oldExists, oldActive := runIP, ifaceExists, unitActive
	runIP = f.run
	ifaceExists = func(string) bool { return exists }
	unitActive = func(context.Context, string) bool { return true }
	t.Cleanup(func() { runIP, ifaceExists, unitActive = oldRun, oldExists, oldActive })
}

func TestEnsureRulesSkipsWhileUnitNotActive(t *testing.T) {
	f := &fakeIP{rules: rulesFlushed}
	withFake(t, f, true)
	var asked string
	unitActive = func(_ context.Context, unit string) bool { asked = unit; return false }
	added, err := EnsureRules(context.Background(), "wg-cascade", "awg-quick@wg-cascade", 51820)
	if err != nil || len(added) != 0 || asked != "awg-quick@wg-cascade" {
		t.Fatalf("touched routing while awg-quick starting/stopping: added %v err %v unit %q", added, err, asked)
	}
	for _, c := range f.calls {
		if strings.Contains(c, " add ") || strings.Contains(c, "replace") {
			t.Fatalf("write issued: %v", f.calls)
		}
	}
}

func TestEnsureRulesRestoresFlushed(t *testing.T) {
	f := &fakeIP{rules: rulesFlushed, routes: "default dev wg-cascade scope link\n"}
	withFake(t, f, true)
	added, err := EnsureRules(context.Background(), "wg-cascade", "", 51820)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("want 2 rules added, got %v", added)
	}
	st, _ := ReadRules(context.Background(), "wg-cascade", 51820)
	if !st.OK() {
		t.Fatalf("not restored: %+v", st)
	}
	// Second pass is a no-op: no duplicate rules.
	added, err = EnsureRules(context.Background(), "wg-cascade", "", 51820)
	if err != nil || len(added) != 0 {
		t.Fatalf("second pass added %v err %v", added, err)
	}
}

func TestEnsureRulesRestoresRoute(t *testing.T) {
	f := &fakeIP{rules: rulesOK}
	withFake(t, f, true)
	added, err := EnsureRules(context.Background(), "wg-cascade", "", 51820)
	if err != nil || len(added) != 1 || !strings.HasPrefix(added[0], "route") {
		t.Fatalf("added %v err %v", added, err)
	}
}

func TestEnsureRulesSkipsWhenTunnelDown(t *testing.T) {
	f := &fakeIP{rules: rulesFlushed}
	withFake(t, f, false)
	added, err := EnsureRules(context.Background(), "wg-cascade", "", 51820)
	if err != nil || len(added) != 0 || len(f.calls) != 0 {
		t.Fatalf("touched routing with tunnel down: added %v calls %v err %v", added, f.calls, err)
	}
}

func TestEnsureRulesReportsAddFailure(t *testing.T) {
	f := &fakeIP{rules: rulesFlushed, routes: "default dev wg-cascade\n", failAdd: true}
	withFake(t, f, true)
	if _, err := EnsureRules(context.Background(), "wg-cascade", "", 51820); err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteIfChanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "networkd.conf.d", "10-vlr.conf")
	created, err := writeIfChanged(p, networkdDropInBody)
	if err != nil || !created {
		t.Fatalf("first write: created=%v err=%v", created, err)
	}
	created, err = writeIfChanged(p, networkdDropInBody)
	if err != nil || created {
		t.Fatalf("rewrite of identical content: created=%v err=%v", created, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "ManageForeignRoutingPolicyRules=no") {
		t.Fatalf("drop-in content: %s", b)
	}
}

func TestFormatEgress(t *testing.T) {
	out := FormatEgress(RuleState{}, nil, EgressResult{Marked: "158.160.236.81", Direct: "158.160.236.81"})
	if !strings.Contains(out, "каскад обходится") || strings.Count(out, "[FAIL]") != 4 {
		t.Fatalf("bypass not reported:\n%s", out)
	}
	ok := EgressResult{Marked: "94.237.13.233", Direct: "158.160.236.81"}
	out = FormatEgress(RuleState{true, true, true}, nil, ok)
	if strings.Contains(out, "[FAIL]") || !ok.OK() {
		t.Fatalf("healthy reported as failing:\n%s", out)
	}
	if r := (EgressResult{Err: errors.New("x")}); r.OK() || r.Bypassed() {
		t.Fatal("marked lookup error must be inconclusive")
	}
	// Direct lookup failing must not turn into a false OK (review finding).
	r := EgressResult{Marked: "158.160.236.81", DirectErr: errors.New("timeout")}
	if r.OK() || r.Bypassed() || !r.Inconclusive() {
		t.Fatalf("direct error: OK=%v Bypassed=%v", r.OK(), r.Bypassed())
	}
	if out := FormatEgress(RuleState{true, true, true}, nil, r); strings.Contains(out, "[OK] (") || !strings.Contains(out, "[?]") {
		t.Fatalf("inconclusive shown wrong:\n%s", out)
	}
}
