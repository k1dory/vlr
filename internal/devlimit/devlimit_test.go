package devlimit

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func ips(bans []Ban) map[string][]string {
	out := map[string][]string{}
	for _, b := range bans {
		out[b.User] = append(out[b.User], b.IP)
	}
	return out
}

func TestUnderLimitNoBans(t *testing.T) {
	l := New(Policy{Max: 2, Ban: time.Minute})
	bans, changed := l.Step(t0, map[string][]string{"a": {"1.1.1.1", "2.2.2.2"}})
	if len(bans) != 0 || changed {
		t.Fatalf("want no bans, got %v changed=%v", bans, changed)
	}
}

func TestNewestOverLimitIsBanned(t *testing.T) {
	l := New(Policy{Max: 2, Ban: time.Minute})
	l.Step(t0, map[string][]string{"a": {"1.1.1.1", "2.2.2.2"}})
	bans, changed := l.Step(t0.Add(10*time.Second), map[string][]string{"a": {"1.1.1.1", "2.2.2.2", "3.3.3.3"}})
	if !changed || !reflect.DeepEqual(ips(bans), map[string][]string{"a": {"3.3.3.3"}}) {
		t.Fatalf("want 3.3.3.3 banned, got %v changed=%v", bans, changed)
	}
	if want := t0.Add(70 * time.Second); !bans[0].Until.Equal(want) {
		t.Fatalf("until=%v want %v", bans[0].Until, want)
	}
}

func TestLimitIsPerUser(t *testing.T) {
	l := New(Policy{Max: 1, Ban: time.Minute})
	// Same IP for two users (CGNAT) must not count across users.
	bans, _ := l.Step(t0, map[string][]string{"a": {"9.9.9.9"}, "b": {"9.9.9.9"}})
	if len(bans) != 0 {
		t.Fatalf("shared IP across users must not ban: %v", bans)
	}
}

func TestBannedIPDoesNotTakeSlotAndStaysBannedUntilExpiry(t *testing.T) {
	l := New(Policy{Max: 1, Ban: time.Minute})
	l.Step(t0, map[string][]string{"a": {"1.1.1.1"}})
	l.Step(t0.Add(time.Second), map[string][]string{"a": {"1.1.1.1", "2.2.2.2"}})
	// Still both online, ban not expired: no change.
	bans, changed := l.Step(t0.Add(30*time.Second), map[string][]string{"a": {"1.1.1.1", "2.2.2.2"}})
	if changed || len(bans) != 1 {
		t.Fatalf("want stable single ban, got %v changed=%v", bans, changed)
	}
}

func TestDeviceSwitchRecoversAfterOldIPLeaves(t *testing.T) {
	l := New(Policy{Max: 1, Ban: time.Minute})
	l.Step(t0, map[string][]string{"a": {"1.1.1.1"}})                             // Wi-Fi
	l.Step(t0.Add(time.Second), map[string][]string{"a": {"1.1.1.1", "2.2.2.2"}}) // LTE appears, Wi-Fi lingers -> LTE banned
	// Old IP gone; ban expires -> LTE becomes the allowed device.
	bans, changed := l.Step(t0.Add(2*time.Minute), map[string][]string{"a": {"2.2.2.2"}})
	if len(bans) != 0 || !changed {
		t.Fatalf("want ban lifted, got %v changed=%v", bans, changed)
	}
	// And it is not re-banned on the next tick.
	if bans, _ := l.Step(t0.Add(3*time.Minute), map[string][]string{"a": {"2.2.2.2"}}); len(bans) != 0 {
		t.Fatalf("re-banned: %v", bans)
	}
}

func TestStillOverLimitAfterExpiryIsRebanned(t *testing.T) {
	l := New(Policy{Max: 1, Ban: time.Minute})
	l.Step(t0, map[string][]string{"a": {"1.1.1.1"}})
	l.Step(t0.Add(time.Second), map[string][]string{"a": {"1.1.1.1", "2.2.2.2"}})
	bans, _ := l.Step(t0.Add(2*time.Minute), map[string][]string{"a": {"1.1.1.1", "2.2.2.2"}})
	if !reflect.DeepEqual(ips(bans), map[string][]string{"a": {"2.2.2.2"}}) {
		t.Fatalf("want 2.2.2.2 re-banned, got %v", bans)
	}
}

func TestExemptUserNeverBanned(t *testing.T) {
	l := New(Policy{Max: 1, Ban: time.Minute, Exempt: []string{"admin@x"}})
	bans, _ := l.Step(t0, map[string][]string{
		"admin@x": {"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"},
		"u@x":     {"5.5.5.5", "6.6.6.6"},
	})
	if !reflect.DeepEqual(ips(bans), map[string][]string{"u@x": {"6.6.6.6"}}) {
		t.Fatalf("want only u@x banned, got %v", bans)
	}
}

func TestDisabledPolicyNeverBans(t *testing.T) {
	l := New(Policy{Max: 0})
	bans, _ := l.Step(t0, map[string][]string{"a": {"1.1.1.1", "2.2.2.2", "3.3.3.3"}})
	if len(bans) != 0 {
		t.Fatalf("disabled policy banned: %v", bans)
	}
}

func TestSimultaneousNewIPsBanDeterministically(t *testing.T) {
	l := New(Policy{Max: 2, Ban: time.Minute})
	bans, _ := l.Step(t0, map[string][]string{"a": {"3.3.3.3", "1.1.1.1", "2.2.2.2"}})
	if !reflect.DeepEqual(ips(bans), map[string][]string{"a": {"3.3.3.3"}}) {
		t.Fatalf("want lexically-last IP banned on a tie, got %v", bans)
	}
}

func TestParseOnline(t *testing.T) {
	raw := []byte(`{"users":[{"email":"a@x","ips":[{"ip":"10.0.0.1","lastSeen":1},{"ip":"10.0.0.2","lastSeen":2}]},{"email":"b@x","ips":[]}]}`)
	got, err := parseOnline(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string][]string{"a@x": {"10.0.0.1", "10.0.0.2"}}) {
		t.Fatalf("got %v", got)
	}
	if got, err := parseOnline([]byte(`{}`)); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
}

func TestBuildRulesPutsBansFirst(t *testing.T) {
	cfg := []byte(`{"routing":{"rules":[{"type":"field","inboundTag":["api-in"],"outboundTag":"api"},{"type":"field","domain":["domain:ru"],"outboundTag":"egress-ru"}]}}`)
	out, err := BuildRules(cfg, []Ban{{User: "a@x", IP: "1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	r := got.Routing.Rules
	if len(r) != 3 || r[0]["outboundTag"] != "block" || r[1]["outboundTag"] != "api" || r[2]["outboundTag"] != "egress-ru" {
		t.Fatalf("bad order: %v", r)
	}
	if r[0]["ruleTag"] != RuleTag("a@x", "1.2.3.4") {
		t.Fatalf("ruleTag %v", r[0]["ruleTag"])
	}
}

func TestParseOldXrayPerUserFormat(t *testing.T) {
	emails, err := parseOnlineUsers([]byte(`{"users":["user>>>a@x>>>online","user>>>b@x>>>online","junk"]}`))
	if err != nil || !reflect.DeepEqual(emails, []string{"a@x", "b@x"}) {
		t.Fatalf("emails=%v err=%v", emails, err)
	}
	ips, err := parseUserIPs([]byte(`{"ips":{"10.0.0.2":2,"10.0.0.1":1},"name":"user>>>a@x>>>online"}`))
	if err != nil || !reflect.DeepEqual(ips, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Fatalf("ips=%v err=%v", ips, err)
	}
}
