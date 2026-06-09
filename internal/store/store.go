// Package store persists node-local state for standalone and child roles.
//
// We deliberately avoid an external database here: a node's local state is tiny
// (a list of clients, a usage counter) and a JSON file under DataDir with a
// process-wide mutex is enough and keeps the binary dependency-free. The main
// server aggregates everything and is where a real PostgreSQL lives.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// User is one VPN client provisioned on a node.
type User struct {
	UUID       string    `json:"uuid"`        // VLESS id
	Email      string    `json:"email"`       // label / accounting key
	TelegramID int64     `json:"telegram_id"` // owner's Telegram user id (0 = unset)
	ShortID    string    `json:"short_id"`    // Reality short id handed to this user
	Profile    string    `json:"profile"`     // "mobile" | "desktop" (decides flow)
	CreatedAt  time.Time `json:"created_at"`
	Enabled    bool      `json:"enabled"`
	// RxBytes/TxBytes are the last-known per-user counters (filled by the stats
	// poller against Xray's stats API). Monotonic within a node lifetime.
	RxBytes int64 `json:"rx_bytes"`
	TxBytes int64 `json:"tx_bytes"`
}

// State is the full node-local document.
type State struct {
	Users []User `json:"users"`
	// ConfigVersion increments on every mutation. The child heartbeat reports it
	// so the main server knows when to pull a fresh config.
	ConfigVersion int64 `json:"config_version"`
}

// Store is a JSON-file-backed state store, safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	st   State
}

// Open loads (or creates) the state file at <dataDir>/state.json.
func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data dir is empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	s := &Store{path: filepath.Join(dataDir, "state.json")}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // fresh
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return s, nil
}

func (s *Store) flushLocked() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// AddUser inserts a user and bumps ConfigVersion.
func (s *Store) AddUser(u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.st.Users {
		if e.Email == u.Email {
			return fmt.Errorf("user %q already exists", u.Email)
		}
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	u.Enabled = true
	s.st.Users = append(s.st.Users, u)
	s.st.ConfigVersion++
	return s.flushLocked()
}

// RemoveUser deletes a user by email and bumps ConfigVersion.
func (s *Store) RemoveUser(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.st.Users[:0]
	found := false
	for _, e := range s.st.Users {
		if e.Email == email {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return fmt.Errorf("user %q not found", email)
	}
	s.st.Users = out
	s.st.ConfigVersion++
	return s.flushLocked()
}

// Users returns a copy of the current user list.
func (s *Store) Users() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, len(s.st.Users))
	copy(out, s.st.Users)
	return out
}

// ConfigVersion returns the current version counter.
func (s *Store) ConfigVersion() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.ConfigVersion
}

// TotalBytes returns the summed Rx+Tx across all users — the cheap aggregate the
// child heartbeat reports and the main uses for delta-triggered pulls.
func (s *Store) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t int64
	for _, u := range s.st.Users {
		t += u.RxBytes + u.TxBytes
	}
	return t
}

// UpdateTraffic sets per-user counters (called by the stats poller).
func (s *Store) UpdateTraffic(email string, rx, tx int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Users {
		if s.st.Users[i].Email == email {
			s.st.Users[i].RxBytes = rx
			s.st.Users[i].TxBytes = tx
			return s.flushLocked()
		}
	}
	return fmt.Errorf("user %q not found", email)
}
