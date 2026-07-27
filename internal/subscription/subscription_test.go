package subscription

import (
	"testing"

	"github.com/k1dory/vlr/internal/store"
)

func TestPublicURL(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		token string
		want  string
	}{
		{"normal", "https://link.infrashark.tech", "abc123", "https://link.infrashark.tech/base64/abc123"},
		{"trailing slash trimmed", "https://link.infrashark.tech/", "abc123", "https://link.infrashark.tech/base64/abc123"},
		{"no base url => empty", "", "abc123", ""},
		{"no token => empty", "https://link.infrashark.tech", "", ""},
		{"both empty => empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PublicURL(tt.base, store.User{SubToken: tt.token})
			if got != tt.want {
				t.Errorf("PublicURL(%q, token=%q) = %q, want %q", tt.base, tt.token, got, tt.want)
			}
		})
	}
}
