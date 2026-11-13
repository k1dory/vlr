package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

// Regression: the daemon (one Store) and the CLI (another Store on the same
// file) used to clobber each other — the daemon's traffic flush rewrote the file
// from its stale in-memory list and dropped users the CLI had just added.
func TestTwoStoresOnOneFileDoNotLoseWrites(t *testing.T) {
	dir := t.TempDir()
	daemon, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.AddUser(User{UUID: "u1", Email: "a@x"}); err != nil {
		t.Fatal(err)
	}
	cli, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.AddUser(User{UUID: "u2", Email: "b@x"}); err != nil {
		t.Fatal(err)
	}
	// Daemon flushes traffic with its old view: must not drop u2.
	unknown, err := daemon.UpdateTrafficBatch(map[string]Traffic{"a@x": {Rx: 5, Tx: 6}, "ghost@x": {Rx: 1}})
	if err != nil || unknown != 1 {
		t.Fatalf("batch: unknown=%d err=%v", unknown, err)
	}
	fresh, _ := Open(dir)
	got := fresh.Users()
	if len(got) != 2 {
		t.Fatalf("lost a user: %+v", got)
	}
	if u, _ := fresh.FindUser("a@x"); u.RxBytes != 5 || u.TxBytes != 6 {
		t.Fatalf("traffic not saved: %+v", u)
	}
	// Daemon reads see the CLI's change without reopening.
	if _, ok := daemon.FindUser("b@x"); !ok {
		t.Fatal("daemon did not pick up CLI-added user")
	}
	// CLI removal is visible to the daemon too.
	if err := cli.RemoveUser("a@x"); err != nil {
		t.Fatal(err)
	}
	if len(daemon.Users()) != 1 {
		t.Fatalf("daemon still sees removed user: %+v", daemon.Users())
	}
	if daemon.ConfigVersion() != 3 {
		t.Fatalf("config version = %d, want 3 (add, add, remove)", daemon.ConfigVersion())
	}
}

func TestUpdateTrafficBatchWritesOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.AddUser(User{UUID: "u1", Email: "a@x"})
	_, _ = s.UpdateTrafficBatch(map[string]Traffic{"a@x": {Rx: 1, Tx: 1}})
	fi1, _ := os.Stat(filepath.Join(dir, "state.json"))
	time.Sleep(20 * time.Millisecond)
	_, _ = s.UpdateTrafficBatch(map[string]Traffic{"a@x": {Rx: 1, Tx: 1}})
	fi2, _ := os.Stat(filepath.Join(dir, "state.json"))
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("unchanged counters rewrote the state file")
	}
}

// Review pass 3, finding 3: a state file that disappears after the store was
// populated must not read as "no users" (Apply would remove everyone).
func TestSnapshotFailsWhenStateFileVanishes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddUser(User{UUID: "u1", Email: "a@x"})
	if err := os.Rename(filepath.Join(dir, "state.json"), filepath.Join(dir, "moved.json")); err != nil {
		t.Fatal(err)
	}
	if u, err := s.Snapshot(); err == nil {
		t.Fatalf("want error, got %d users", len(u))
	}
	if err := s.AddUser(User{UUID: "u2", Email: "b@x"}); err == nil {
		t.Fatal("mutation on a vanished state file must fail, not start from empty")
	}
	// A new process (CLI) must not recreate an empty store either.
	if _, err := Open(dir); err == nil {
		t.Fatal("Open recreated an empty store on an initialized node")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Fatal("state.json was recreated")
	}
}

func TestOpenAddsMarkerToExistingNode(t *testing.T) {
	dir := t.TempDir()
	// Pre-marker node: state file exists, no marker.
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"users":[{"uuid":"u1","email":"a@x","sub_token":"t"}],"config_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".initialized")); err != nil {
		t.Fatal("marker not written for an existing node")
	}
	os.Remove(filepath.Join(dir, "state.json"))
	if _, err := Open(dir); err == nil {
		t.Fatal("lost state on a migrated node must fail")
	}
}

func TestOpenCreatesEmptyStateFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := s.Snapshot(); err != nil || len(u) != 0 {
		t.Fatalf("fresh store snapshot: %v %v", u, err)
	}
}
