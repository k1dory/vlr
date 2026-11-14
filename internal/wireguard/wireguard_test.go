package wireguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k1dory/vlr/internal/config"
)

func TestEntryAWGAndLegacy(t *testing.T) {
	a, err := config.NewAWGConfig()
	if err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Cascade: config.CascadeConfig{Enabled: true, Transport: "awg", AWG: a, PrivateKey: "private", ExitPublicKey: "public", ExitEndpoint: "example.com:51820"}}
	s, err := RenderEntry(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{a.Render(), "Table = off", "ip rule add fwmark 51820 table 51820", "ip rule add oif %i table 51820", "while ip rule del oif %i table 51820 2>/dev/null; do :; done", "ip route replace default dev %i table 51820"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(s, "not fwmark") {
		t.Error("must not capture management traffic")
	}
	c.Cascade.Transport = ""
	s, err = RenderEntry(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "Jc =") {
		t.Error("legacy WG must not receive AWG fields")
	}
	c.Cascade.Transport = "awg"
	c.Cascade.AWG = nil
	if _, err := RenderEntry(c); err == nil {
		t.Error("missing AWG parameters accepted")
	}
}

// The hooks the main node carried on 13.11.2026 (vlr <= v0.4.0).
const legacyEntryConf = `# vlr cascade — RU entry side (ru-yc-msk)
[Interface]
Address = 10.66.0.2/32
PrivateKey = x
MTU = 1420
Table = off
PostUp = ip route add default dev %i table 51820; ip rule add fwmark 51820 table 51820
PostUp = ip rule add oif %i table 51820
PostDown = ip rule del fwmark 51820 table 51820 2>/dev/null || true
PostDown = ip rule del oif %i table 51820 2>/dev/null || true

[Peer]
PublicKey = y
Endpoint = 94.237.13.233:51820
`

func TestMigrateEntryHooksLegacy(t *testing.T) {
	out, changed, idem := MigrateEntryHooksData(legacyEntryConf, 51820)
	if !changed || !idem {
		t.Fatalf("changed=%v idempotent=%v", changed, idem)
	}
	if strings.Contains(out, "ip route add") || strings.Contains(out, "|| true") {
		t.Fatalf("legacy hooks left:\n%s", out)
	}
	for _, h := range EntryHooks(51820) {
		if !strings.Contains(out, h+"\n") {
			t.Fatalf("missing %q:\n%s", h, out)
		}
	}
	for _, keep := range []string{"PrivateKey = x\n", "Table = off\nPostUp", "\n\n[Peer]\nPublicKey = y\nEndpoint = 94.237.13.233:51820\n"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("lost %q:\n%s", keep, out)
		}
	}
	// Second pass: nothing to do.
	if out2, changed, idem := MigrateEntryHooksData(out, 51820); changed || !idem || out2 != out {
		t.Fatalf("not stable: changed=%v idem=%v", changed, idem)
	}
}

func TestMigrateEntryHooksLeavesCustomHooks(t *testing.T) {
	custom := strings.Replace(legacyEntryConf, "PostUp = ip rule add oif %i table 51820", "PostUp = /usr/local/bin/my-hook %i", 1)
	out, changed, idem := MigrateEntryHooksData(custom, 51820)
	if changed || idem || out != custom {
		t.Fatalf("custom hooks touched: changed=%v idem=%v", changed, idem)
	}
	if _, changed, idem := MigrateEntryHooksData("[Interface]\nAddress = 1\n", 51820); changed || idem {
		t.Fatal("config without hooks must not count as idempotent")
	}
}

func TestMigrateEntryHooksFileKeepsMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wg-cascade.conf")
	if err := os.WriteFile(p, []byte(legacyEntryConf), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, idem, err := MigrateEntryHooks(p, 51820)
	if err != nil || !changed || !idem {
		t.Fatalf("changed=%v idem=%v err=%v", changed, idem, err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if n := e.Name(); n != "wg-cascade.conf" && n != "wg-cascade.conf.lock" {
			t.Fatalf("temp file left behind: %v", entries)
		}
	}
}

func TestRenderEntryHooksAreMigrated(t *testing.T) {
	c := &config.Config{NodeID: "n", Cascade: config.CascadeConfig{Enabled: true, Address: "10.66.0.2/32", PrivateKey: "a", ExitPublicKey: "b", ExitEndpoint: "1.2.3.4:51820"}}
	conf, err := RenderEntry(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, idem := MigrateEntryHooksData(conf, 51820); changed || !idem {
		t.Fatalf("fresh render not canonical: changed=%v idem=%v", changed, idem)
	}
}

func TestMigrateEntryHooksRejectsLowercaseLegacy(t *testing.T) {
	// awg-quick keys are case-insensitive: an unrecognized lowercase "postup"
	// with `route add` must block the rewrite, not be kept next to new hooks.
	conf := strings.Replace(legacyEntryConf,
		"PostUp = ip route add default dev %i table 51820; ip rule add fwmark 51820 table 51820\nPostUp = ip rule add oif %i table 51820\nPostDown = ip rule del fwmark 51820 table 51820 2>/dev/null || true\n",
		"PostDown = ip rule del fwmark 51820 table 51820 2>/dev/null || true\npostup = ip route add default dev %i table 51820\nPostUp = ip rule add oif %i table 51820\n", 1)
	if !strings.Contains(conf, "postup = ") {
		t.Fatal("fixture not applied")
	}
	out, changed, idem := MigrateEntryHooksData(conf, 51820)
	if changed || idem || out != conf {
		t.Fatalf("lowercase hook mishandled: changed=%v idem=%v", changed, idem)
	}
}
