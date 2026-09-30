package config

import "testing"

func TestAWGParameters(t *testing.T) {
	a, err := NewAWGConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	a.H2 = a.H1
	if a.Validate() == nil {
		t.Error("duplicate headers accepted")
	}
}

func TestCascadeTransportCompatibility(t *testing.T) {
	for _, tt := range []struct{ transport, tool, dir string }{
		{"", "wg", "/etc/wireguard"}, {"wg", "wg", "/etc/wireguard"}, {"awg", "awg", "/etc/amnezia/amneziawg"},
	} {
		c := CascadeConfig{Transport: tt.transport}
		if c.Tool() != tt.tool || c.ConfigDir() != tt.dir {
			t.Fatalf("incorrect transport mapping for %q", tt.transport)
		}
	}
}
