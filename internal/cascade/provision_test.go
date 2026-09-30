package cascade

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k1dory/vlr/internal/config"
)

func TestBuildExitScript(t *testing.T) {
	s := BuildExitScript(ExitProvisionParams{
		Iface: "wg-cascade", EUAddress: "10.66.0.1/24", WGPort: 51820,
		WAN: "", RUPublicKey: "RUPUBKEYBASE64=", RUTunnelIP: "10.66.0.2",
	})
	musts := []string{
		"awg genkey",                  // EU generates its own key
		"ListenPort = 51820",          // port wired
		"AllowedIPs = 10.66.0.2/32",   // forward-only: only the RU peer
		"PublicKey = RUPUBKEYBASE64=", // RU pub injected
		"MASQUERADE",                  // NAT egress
		"net.ipv4.ip_forward=1",       // forwarding enabled
		"ip route get 1.1.1.1",        // WAN auto-detect (WAN was empty)
		"VLR_EU_PUBKEY=$EU_PUB",       // pubkey echoed back to caller
		"%i",                          // wg-quick literal interface placeholder survives
	}
	for _, m := range musts {
		if !strings.Contains(s, m) {
			t.Errorf("exit script missing %q", m)
		}
	}
	// The EU private key must never be templated in by us — it is made on the box.
	if strings.Contains(s, "{{") {
		t.Errorf("unreplaced placeholder remains:\n%s", s)
	}
}

func TestExitAWGAutostart(t *testing.T) {
	a, err := config.NewAWGConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		s := BuildExitScript(ExitProvisionParams{Iface: "wg-cascade", EUAddress: "10.66.0.1/24", WGPort: 51820, RUTunnelIP: "10.66.0.2", AWG: a, Autostart: enabled})
		action := "disable"
		if enabled {
			action = "enable"
		}
		for _, want := range []string{a.Render(), "systemctl " + action + " awg-quick@wg-cascade", "systemctl start awg-quick@wg-cascade", "/etc/amnezia/amneziawg/wg-cascade.conf"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(s, "systemctl enable wg-quick@") {
			t.Error("legacy WG must not auto-start")
		}
		stop := strings.Index(s, "systemctl stop awg-quick@")
		write := strings.Index(s, "cat > /etc/amnezia/amneziawg/")
		if stop < 0 || stop > write {
			t.Error("must stop old interface before replacing PostDown hooks")
		}
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("invalid bootstrap shell: %v: %s", err, out)
		}
	}
}

func TestValidateExitParams(t *testing.T) {
	p := ExitProvisionParams{Iface: "wg-cascade", EUAddress: "10.66.0.1/24", WGPort: 51820, RUTunnelIP: "10.66.0.2"}
	if err := ValidateExitParams(p); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../bad", "wg;id", "$(id)", "", "1234567890123456"} {
		p.Iface = bad
		if ValidateExitParams(p) == nil {
			t.Errorf("accepted interface %q", bad)
		}
	}
}

func TestFormatResults(t *testing.T) {
	out := FormatResults([]SiteResult{
		{Host: "claude.ai", OK: true, Code: "200", Dur: time.Second},
		{Host: "exampletest", OK: false, Err: "Time to request failed (30s)"},
	})
	if !strings.Contains(out, "claude.ai") || !strings.Contains(out, "[OK]") {
		t.Errorf("OK row missing:\n%s", out)
	}
	if !strings.Contains(out, "exampletest") || !strings.Contains(out, "[FAIL]") {
		t.Errorf("FAIL row missing:\n%s", out)
	}
	if !strings.Contains(out, "logs: Time to request failed (30s)") {
		t.Errorf("fail log line missing:\n%s", out)
	}
}
