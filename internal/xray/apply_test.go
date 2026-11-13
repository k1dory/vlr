package xray

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

// --- fixtures ---------------------------------------------------------------

type src []store.User

func (s src) Users() []store.User { return s }

func users(emails ...string) src {
	var out src
	for _, e := range emails {
		out = append(out, store.User{UUID: "id-" + e, Email: e, Enabled: true})
	}
	return out
}

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

func render(t *testing.T, c *config.Config, u src) []byte {
	t.Helper()
	b, err := Render(c, u)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func reloaded(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// fakeXray models the running Xray's user list behind the CLI.
type fakeXray struct {
	live      map[string]Client
	down      bool // every API call fails
	silentRmu bool // rmu exits 0 but removes nothing (per-user RPC error)
	failAdu   bool
	calls     []string
}

func newFake(t *testing.T, emails ...string) *fakeXray {
	f := &fakeXray{live: map[string]Client{}}
	for _, e := range emails {
		f.live[e] = Client{ID: "id-" + e}
	}
	orig := runXray
	runXray = f.run
	t.Cleanup(func() { runXray = orig })
	return f
}

func (f *fakeXray) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args[1])
	if f.down {
		return nil, errors.New("connection refused")
	}
	switch args[1] {
	case "inbounduser":
		type acc struct {
			ID   string `json:"id"`
			Flow string `json:"flow,omitempty"`
		}
		type u struct {
			Email   string `json:"email,omitempty"`
			Account acc    `json:"account"`
		}
		resp := struct {
			Users []u `json:"users"`
		}{}
		for e, c := range f.live {
			resp.Users = append(resp.Users, u{Email: e, Account: acc{ID: c.ID, Flow: c.Flow}})
		}
		return json.Marshal(resp)
	case "rmu":
		if !f.silentRmu {
			for _, e := range args[4:] {
				delete(f.live, e)
			}
		}
		return []byte("removed"), nil
	case "adu":
		if f.failAdu {
			return nil, errors.New("adu failed")
		}
		b, err := os.ReadFile(args[3])
		if err != nil {
			return nil, err
		}
		var cfg struct {
			Inbounds []struct {
				Settings struct {
					Clients []struct{ ID, Email, Flow string } `json:"clients"`
				} `json:"settings"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, err
		}
		for _, c := range cfg.Inbounds[0].Settings.Clients {
			f.live[c.Email] = Client{ID: c.ID, Flow: c.Flow}
		}
		return []byte("added"), nil
	}
	return nil, errors.New("unexpected " + args[1])
}

func (f *fakeXray) emails() []string {
	var out []string
	for e := range f.live {
		out = append(out, e)
	}
	return sortedCopy(out)
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// --- pure helpers -------------------------------------------------------------

func TestDiff(t *testing.T) {
	live := map[string]Client{"a": {ID: "1"}, "b": {ID: "2"}, "c": {ID: "3"}}
	desired := map[string]Client{"a": {ID: "1"}, "b": {ID: "2", Flow: "xtls-rprx-vision"}, "d": {ID: "4"}}
	p := Diff(live, desired)
	if !reflect.DeepEqual(p.Remove, []string{"b", "c"}) {
		t.Fatalf("remove %v", p.Remove)
	}
	var add []string
	for _, c := range p.Add {
		add = append(add, c["email"].(string))
	}
	if !reflect.DeepEqual(add, []string{"b", "d"}) || p.Add[0]["flow"] != "xtls-rprx-vision" {
		t.Fatalf("add %v", p.Add)
	}
	if !Diff(desired, desired).Empty() {
		t.Fatal("identical must be empty")
	}
}

func TestSameExceptClients(t *testing.T) {
	c, _ := testCfg(t)
	a := render(t, c, users("a"))
	if ok, err := SameExceptClients(a, render(t, c, users("a", "b"))); !ok || err != nil {
		t.Fatalf("clients-only change: %v %v", ok, err)
	}
	c2 := *c
	c2.Split.RUDirect = []string{"vk.com", "ok.ru"}
	if ok, _ := SameExceptClients(a, render(t, &c2, users("a"))); ok {
		t.Fatal("routing change reported as clients-only")
	}
	for name, bad := range map[string]string{
		"no inbound":  `{"inbounds":[{"tag":"other"}]}`,
		"dup email":   `{"inbounds":[{"tag":"vless-reality","settings":{"clients":[{"id":"1","email":"a"},{"id":"2","email":"a"}]}}]}`,
		"empty email": `{"inbounds":[{"tag":"vless-reality","settings":{"clients":[{"id":"1"}]}}]}`,
		"not json":    `{`,
		"no settings": `{"inbounds":[{"tag":"vless-reality"}]}`,
	} {
		if _, err := SameExceptClients([]byte(bad), a); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestParseLiveUsersSkipsPlaceholder(t *testing.T) {
	m, err := parseLiveUsers([]byte(`{"users":[{}, {"account":{"id":"1","flow":"xtls-rprx-vision"},"email":"a"}]}`))
	if err != nil || len(m) != 1 || m["a"] != (Client{ID: "1", Flow: "xtls-rprx-vision"}) {
		t.Fatalf("got %v %v", m, err)
	}
}

// --- Apply against a fake running Xray ----------------------------------------

func TestApplyAddsUserLiveWithoutRestart(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	f := newFake(t, "a")
	if err := Apply(c, users("a", "b")); err != nil {
		t.Fatal(err)
	}
	if reloaded(marker) || !reflect.DeepEqual(f.emails(), []string{"a", "b"}) {
		t.Fatalf("restart=%v live=%v", reloaded(marker), f.emails())
	}
	got, _ := os.ReadFile(c.Xray.ConfigPath)
	if string(got) != string(render(t, c, users("a", "b"))) {
		t.Fatal("file not updated")
	}
}

func TestApplyRemovesUserLive(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a", "b")), 0o644)
	f := newFake(t, "a", "b")
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if reloaded(marker) || !reflect.DeepEqual(f.emails(), []string{"a"}) {
		t.Fatalf("restart=%v live=%v", reloaded(marker), f.emails())
	}
}

func TestApplyNoopTouchesNothing(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	f := newFake(t, "a")
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if reloaded(marker) || strings.Join(f.calls, ",") != "inbounduser" {
		t.Fatalf("restart=%v calls=%v", reloaded(marker), f.calls)
	}
}

// Review finding 1: rmu reports per-user failure but exits 0.
func TestApplySilentRPCFailureFallsBackToRestart(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a", "b")), 0o644)
	f := newFake(t, "a", "b")
	f.silentRmu = true
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) {
		t.Fatal("revoked user still live and no restart")
	}
}

func TestApplyAduFailureFallsBackToRestart(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	f := newFake(t, "a")
	f.failAdu = true
	if err := Apply(c, users("a", "b")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) {
		t.Fatal("adu failure must restart")
	}
}

// Review finding 2a: file already says {a} but Xray still runs {a,b} (an earlier
// write failed after a live add). The next Apply must remove b.
func TestApplyComparesAgainstLiveNotFile(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	f := newFake(t, "a", "b")
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if reloaded(marker) || !reflect.DeepEqual(f.emails(), []string{"a"}) {
		t.Fatalf("stale live user kept: live=%v", f.emails())
	}
}

// Review finding 2b: a restart that failed must be retried even if the file
// already matches.
func TestApplyRetriesFailedRestart(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	newFake(t, "a")
	c.Split.RUDirect = append(c.Split.RUDirect, "ok.ru")
	c.Xray.ReloadCmd = "false"
	if err := Apply(c, users("a")); err == nil {
		t.Fatal("want reload error")
	}
	c.Xray.ReloadCmd = "touch " + marker
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) {
		t.Fatal("identical retry after a failed restart must restart")
	}
	if _, err := os.Stat(c.Xray.ConfigPath + ".pending"); !os.IsNotExist(err) {
		t.Fatal("pending marker not cleared after successful restart")
	}
}

func TestApplyXrayDownRestarts(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	f := newFake(t, "a")
	f.down = true
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) {
		t.Fatal("stopped Xray with a matching file must be restarted")
	}
}

func TestApplyRestartsOnNonClientChange(t *testing.T) {
	c, marker := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	f := newFake(t, "a")
	c.Split.RUDirect = append(c.Split.RUDirect, "ok.ru")
	if err := Apply(c, users("a")); err != nil {
		t.Fatal(err)
	}
	if !reloaded(marker) || len(f.calls) != 0 {
		t.Fatalf("routing change: restart=%v calls=%v", reloaded(marker), f.calls)
	}
}

func TestApplyForceAndFirstWriteRestart(t *testing.T) {
	c, marker := testCfg(t)
	newFake(t)
	if err := Apply(c, users("a")); err != nil || !reloaded(marker) {
		t.Fatalf("first write must restart: %v", err)
	}
	os.Remove(marker)
	if err := ApplyForce(c, users("a")); err != nil || !reloaded(marker) {
		t.Fatalf("ApplyForce must restart: %v", err)
	}
}

// Review finding 3: the snapshot is taken under the apply lock, so Apply always
// renders the source's current users, never a list captured before locking.
type countingSrc struct {
	calls int
	u     src
}

func (c *countingSrc) Users() []store.User { c.calls++; return c.u }

func TestApplyReadsUsersUnderLock(t *testing.T) {
	c, _ := testCfg(t)
	_ = os.WriteFile(c.Xray.ConfigPath, render(t, c, users("a")), 0o644)
	f := newFake(t, "a", "b")
	s := &countingSrc{u: users("a")}
	if err := Apply(c, s); err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 || !reflect.DeepEqual(f.emails(), []string{"a"}) {
		t.Fatalf("calls=%d live=%v", s.calls, f.emails())
	}
}
