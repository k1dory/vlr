package store

import "testing"

// TestAddUserGeneratesSubToken checks every created user gets an unguessable
// subscription token, that FindBySubToken resolves it, and that empty never matches.
func TestAddUserGeneratesSubToken(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.AddUser(User{UUID: "u1", Email: "a@x"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	u, ok := st.FindUser("a@x")
	if !ok {
		t.Fatal("user not found after add")
	}
	if len(u.SubToken) != subTokenBytes*2 {
		t.Fatalf("sub token len = %d, want %d", len(u.SubToken), subTokenBytes*2)
	}
	if got, ok := st.FindBySubToken(u.SubToken); !ok || got.UUID != "u1" {
		t.Fatalf("FindBySubToken(%q) = %+v, %v", u.SubToken, got, ok)
	}
	if _, ok := st.FindBySubToken(""); ok {
		t.Fatal("empty token must never match a user")
	}
}

// TestRotateSubToken revokes the old URL while keeping the VLESS UUID stable.
func TestRotateSubToken(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.AddUser(User{UUID: "u1", Email: "a@x"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	before, _ := st.FindUser("a@x")

	newTok, err := st.RotateSubToken("a@x")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newTok == before.SubToken {
		t.Fatal("rotate did not change the token")
	}
	// Old token no longer resolves; new one does; UUID unchanged.
	if _, ok := st.FindBySubToken(before.SubToken); ok {
		t.Fatal("old token still resolves after rotate")
	}
	after, ok := st.FindBySubToken(newTok)
	if !ok || after.UUID != "u1" {
		t.Fatalf("new token resolves to %+v, %v", after, ok)
	}

	if _, err := st.RotateSubToken("nope"); err == nil {
		t.Fatal("rotate of unknown ref should error")
	}
}

// TestBackfillSubTokenOnOpen ensures legacy state (users without a token) gets
// tokens assigned and persisted when the store is opened.
func TestBackfillSubTokenOnOpen(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Simulate a legacy user by injecting one with an empty token and flushing.
	st.mu.Lock()
	st.st.Users = append(st.st.Users, User{UUID: "legacy", Enabled: true})
	_ = st.flushLocked()
	st.mu.Unlock()

	// Reopen: backfill should assign a token.
	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	u, ok := st2.FindUser("legacy")
	if !ok {
		t.Fatal("legacy user missing after reopen")
	}
	if u.SubToken == "" {
		t.Fatal("backfill did not assign a sub token")
	}
}
