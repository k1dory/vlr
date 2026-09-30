package wireguard

import (
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
	for _, want := range []string{a.Render(), "Table = off", "ip rule add fwmark 51820 table 51820", "ip rule add oif %i table 51820", "ip rule del oif %i table 51820"} {
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
