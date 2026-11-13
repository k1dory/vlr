package xray

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

func testCfg(t *testing.T) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "reloaded")
	return &config.Config{
		Entry: config.EntryConfig{
			Host: "1.2.3.4", Port: 443, SNI: "s", PrivateKey: "x", PublicKey: "y",
			ShortIDs: []string{"ab"}, Fingerprint: "randomized",
		},
		Split: config.SplitConfig{RUDirect: []string{"vk.com"}},
		Xray:  config.XrayConfig{ConfigPath: filepath.Join(dir, "config.json"), ReloadCmd: "touch " + marker},
	}, marker
}

func users(emails ...string) []store.User {
	var out []store.User
	for _, e := range emails {
		out = append(out, store.User{UUID: "id-" + e, Email: e, Enabled: true})
	}
	return out
}

func render(t *testing.T, c *config.Config, u []store.User) []byte {
	t.Helper()
	b, err := Render(c, u)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func emailsOf(cl []map[string]any) []string {
	var out []string
	for _, c := range cl {
		out = append(out, c["email"].(string))
	}
	return out
}

func TestPlanLive(t *testing.T) {
	c, _ := testCfg(t)
	base := render(t, c, users("a", "b"))

	p, err := PlanLive(base, base)
	if err != nil || !p.Noop() {
		t.Fatalf("identical: %+v %v", p, err)
	}

	p, _ = PlanLive(base, render(t, c, users("a", "b", "c")))
	if !p.ClientsOnly || len(p.Remove) != 0 || !reflect.DeepEqual(emailsOf(p.Add), []string{"c"}) {
		t.Fatalf("add: %+v", p)
	}

	p, _ = PlanLive(base, render(t, c, users("a")))
	if !p.ClientsOnly || !reflect.DeepEqual(p.Remove, []string{"b"}) || len(p.Add) != 0 {
		t.Fatalf("remove: %+v", p)
	}

	// Changed client (vision flow) = remove + add of the same email.
	u := users("a", "b")
	u[1].Profile = "vision"
	p, _ = PlanLive(base, render(t, c, u))
	if !reflect.DeepEqual(p.Remove, []string{"b"}) || !reflect.DeepEqual(emailsOf(p.Add), []string{"b"}) {
		t.Fatalf("change: %+v", p)
	}

	// Non-client change (split list) must not be patched live.
	c2 := *c
	c2.Split.RUDirect = []string{"vk.com", "ok.ru"}
	p, _ = PlanLive(base, render(t, &c2, users("a", "b")))
	if p.ClientsOnly || p.Noop() {
		t.Fatalf("routing change treated as clients-only: %+v", p)
	}
}

func TestPlanLiveRejectsUnsafeConfigs(t *testing.T) {
	good := `{"inbounds":[{"tag":"vless-reality","settings":{"clients":[{"id":"1","email":"a"}]}}]}`
	for name, bad := range map[string]string{
		"no inbound":  `{"inbounds":[{"tag":"other"}]}`,
		"dup email":   `{"inbounds":[{"tag":"vless-reality","settings":{"clients":[{"id":"1","email":"a"},{"id":"2","email":"a"}]}}]}`,
		"empty email": `{"inbounds":[{"tag":"vless-reality","settings":{"clients":[{"id":"1"}]}}]}`,
		"not json":    `{`,
		"no settings": `{"inbounds":[{"tag":"vless-reality"}]}`,
	} {
		if _, err := PlanLive([]byte(bad), []byte(good)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// stubXray records CLI calls and optionally fails.
func stubXray(t *testing.T, fail error) *[][]string {
	t.Helper()
	var calls [][]string
	orig := runXray
	runXray = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return fail
	}
	t.Cleanup(func() { runXray = orig })
	return &calls
}

func reloaded(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

func TestApplyAddsUserLiveWithoutRestart(t *testing.T) {
	c, marker := testCfg(t)
	if err := os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := stubXray(t, nil)
	if err := Apply(c, users("a", "b")); err != nil {
		t.Fatal(err)
	}
	if reloaded(marker) {
		t.Fatal("xray restarted on a user-only change")
	}
	if len(*calls) != 1 || (*calls)[0][1] != "adu" {
		t.Fatalf("want one adu call, got %v", *calls)
	}
	got, _ := os.ReadFile(c.Xray.ConfigPath)
	if string(got) != string(render(t, c, users("a", "b"))) {
		t.Fatal("config file not updated after live apply")
	}
}

func TestApplyRemovesUserLive(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a", "b")), 0o644)
	calls := stubXray(t, nil)
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if reloaded(marker) || len(*calls) != 1 || (*calls)[0][1] != "rmu" || (*calls)[0][len((*calls)[0])-1] != "b" {
		t.Fatalf("want rmu b without restart, calls=%v reloaded=%v", *calls, reloaded(marker))
	}
}

func TestApplyNoopWhenUnchanged(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	calls := stubXray(t, nil)
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if reloaded(marker) || len(*calls) != 0 {
		t.Fatalf("unchanged config touched xray: calls=%v reloaded=%v", *calls, reloaded(marker))
	}
}

func TestApplyFallsBackToRestartWhenAPIFails(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	stubXray(t, errors.New("connection refused"))
	if err := Apply(c, users("a", "b")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) {
		t.Fatal("API failure must fall back to restart")
	}
	got, _ := os.ReadFile(c.Xray.ConfigPath)
	if !strings.Contains(string(got), `"id-b"`) {
		t.Fatal("config not written on fallback")
	}
}

func TestApplyRestartsOnNonClientChange(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	calls := stubXray(t, nil)
	c.Split.RUDirect = append(c.Split.RUDirect, "ok.ru")
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) || len(*calls) != 0 {
		t.Fatalf("routing change: want restart and no API calls, calls=%v", *calls)
	}
}

func TestApplyForceAlwaysRestarts(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	stubXray(t, nil)
	if err := ApplyForce(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) {
		t.Fatal("ApplyForce must restart")
	}
}

func TestApplyFirstWriteRestarts(t *testing.T) {
	c, marker := testCfg(t)
	stubXray(t, nil)
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) {
		t.Fatal("no previous config: must write and restart")
	}
}
