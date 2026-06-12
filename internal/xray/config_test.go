package xray

import (
	"encoding/json"
	"testing"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/store"
)

func TestBuildRoutingRules_Split(t *testing.T) {
	c := &config.Config{
		Split: config.SplitConfig{
			RUDirect:  []string{"sberbank.ru", "full:lk.mos.ru", ""},
			GeositeRU: []string{"category-ru"},
		},
	}
	rules := buildRoutingRules(c)

	// Find the egress-ru rule and inspect its domain matchers.
	var dom []string
	for _, r := range rules {
		m := r.(map[string]any)
		if m["outboundTag"] == "egress-ru" {
			for _, d := range m["domain"].([]string) {
				dom = append(dom, d)
			}
		}
	}
	want := map[string]bool{
		"domain:sberbank.ru":  false, // bare host gets domain: prefix
		"full:lk.mos.ru":      false, // explicit matcher passes through
		"geosite:category-ru": false, // geosite group added
	}
	for _, d := range dom {
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing RU-direct matcher %q (got %v)", k, dom)
		}
	}
}

func TestStatID_UniquePerUser(t *testing.T) {
	withEmail := store.User{UUID: "uuid-1", Email: "a@x"}
	noEmail := store.User{UUID: "uuid-2"}
	if got := StatID(withEmail); got != "a@x" {
		t.Errorf("StatID(with email) = %q, want email", got)
	}
	if got := StatID(noEmail); got != "uuid-2" {
		t.Errorf("StatID(no email) = %q, want uuid (no empty-string collision)", got)
	}
	if StatID(noEmail) == "" {
		t.Error("StatID must never be empty (would collide in Xray stats)")
	}
}

func TestRender_StatIDIsClientEmail(t *testing.T) {
	c := &config.Config{Entry: config.EntryConfig{
		Host: "1.2.3.4", Port: 443, SNI: "s", PrivateKey: "x", PublicKey: "y",
		ShortIDs: []string{"ab"}, Fingerprint: "randomized",
	}}
	b, _ := Render(c, []store.User{{UUID: "uuid-2", Enabled: true}}) // no email
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	in := got["inbounds"].([]any)[0].(map[string]any)
	clients := in["settings"].(map[string]any)["clients"].([]any)
	email := clients[0].(map[string]any)["email"].(string)
	if email != "uuid-2" {
		t.Errorf("rendered client email = %q, want UUID fallback for stats", email)
	}
}

func TestRender_HasBothEgress(t *testing.T) {
	c := &config.Config{
		Entry: config.EntryConfig{
			Host: "1.2.3.4", Port: 443, SNI: "console.yandex.cloud",
			PrivateKey: "x", PublicKey: "y", ShortIDs: []string{"ab"}, Fingerprint: "randomized",
		},
		Split: config.SplitConfig{RUDirect: []string{"vk.com"}},
	}
	b, err := Render(c, []store.User{{UUID: "u", Email: "e", Enabled: true, Profile: "mobile"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("render is not valid JSON: %v", err)
	}
	tags := map[string]bool{}
	for _, o := range got["outbounds"].([]any) {
		tags[o.(map[string]any)["tag"].(string)] = true
	}
	for _, want := range []string{"egress", "egress-ru", "block"} {
		if !tags[want] {
			t.Errorf("missing outbound %q", want)
		}
	}
}
