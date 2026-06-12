// Package protocol defines the wire types shared by the child node and the main
// server, plus the delta-pull decision logic.
//
// Model: "push heartbeat + delta-triggered pull".
//
//   - The child PUSHES a cheap Heartbeat every ~20s. It is tiny and constant
//     size, so the main always knows liveness — a missed heartbeat means the
//     node (or its internet) is down. This closes the blind spot of a pure pull
//     model, which can't tell "nothing changed" from "node dead".
//   - The main PULLS the heavy detail (per-user traffic, full config) only when
//     a trigger fires: the traffic counter moved past a threshold, the config
//     version changed, the node just came back, or a periodic reconcile is due.
//     So we never hammer the node with constant heavy polling, and never lose
//     state on the main.
package protocol

import "time"

// Heartbeat is the cheap, fixed-shape liveness+summary push from child to main.
type Heartbeat struct {
	NodeID        string `json:"node_id"`
	Seq           uint64 `json:"seq"`            // monotonic per child process
	SentUnix      int64  `json:"sent_unix"`      // child clock, for skew checks
	Healthy       bool   `json:"healthy"`        // cascade up & xray running
	CascadeUp     bool   `json:"cascade_up"`     // RU->EU WG handshake fresh
	UserCount     int    `json:"user_count"`     // number of enabled users
	ConfigVersion int64  `json:"config_version"` // bumps on any user/config change
	TotalBytes    int64  `json:"total_bytes"`    // monotonic sum of all user Rx+Tx
}

// PullResponse is the heavy detail the main fetches on demand.
type PullResponse struct {
	NodeID        string        `json:"node_id"`
	ConfigVersion int64         `json:"config_version"`
	Entry         EntrySnapshot `json:"entry"` // public Reality params, for central link rebuild
	Users         []UserDetail  `json:"users"`
	XrayConfig    string        `json:"xray_config,omitempty"` // optional full config snapshot
}

// EntrySnapshot is the PUBLIC Reality entry parameters the main needs to rebuild
// a client's vless:// link centrally. It carries only what already appears in a
// client config — never the server private key.
type EntrySnapshot struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	SNI         string   `json:"sni"`
	PublicKey   string   `json:"public_key"`
	Fingerprint string   `json:"fingerprint"`
	ShortIDs    []string `json:"short_ids"`
}

// UserDetail is one user's full accounting record. ShortID is the per-user Reality
// short id, included so the main can rebuild the exact vless:// link the node
// issued without re-deriving it.
type UserDetail struct {
	UUID       string `json:"uuid"`
	Email      string `json:"email"`
	TelegramID int64  `json:"telegram_id"`
	ExternalID string `json:"external_id"`
	Profile    string `json:"profile"`
	ShortID    string `json:"short_id"`
	Enabled    bool   `json:"enabled"`
	RxBytes    int64  `json:"rx_bytes"`
	TxBytes    int64  `json:"tx_bytes"`
}

// NodeView is the main server's per-child running state. It is the basis for the
// pull decision and is updated on every heartbeat and every successful pull.
type NodeView struct {
	NodeID string

	LastSeen  time.Time // wall clock of last heartbeat received
	LastSeq   uint64
	Healthy   bool
	CascadeUp bool
	Down      bool // set by the main when heartbeats stop

	// Last values the main has *pulled* (authoritative detail).
	PulledConfigVersion int64
	PulledTotalBytes    int64
	LastPull            time.Time

	// Latest values the main has *heard* via heartbeat (may be ahead of pulled).
	HeardConfigVersion int64
	HeardTotalBytes    int64
}

// PullDecision explains why (or why not) a pull should happen now.
type PullDecision struct {
	Pull   bool
	Reason string
}

// Thresholds parameterises ShouldPull. Zero values fall back to sane defaults.
type Thresholds struct {
	// ByteDelta: pull once HeardTotalBytes - PulledTotalBytes exceeds this.
	// Captures "meaningful traffic happened since we last reconciled" without
	// pulling on every byte.
	ByteDelta int64
	// ReconcileEvery forces a pull this long after the last one regardless of
	// deltas — a safety net for any trigger we failed to catch.
	ReconcileEvery time.Duration
}

func (t Thresholds) withDefaults() Thresholds {
	if t.ByteDelta <= 0 {
		t.ByteDelta = 256 << 20 // 256 MiB
	}
	if t.ReconcileEvery <= 0 {
		t.ReconcileEvery = 10 * time.Minute
	}
	return t
}

// ShouldPull is the core decision. It is pure (no I/O), so it is unit-testable
// and deterministic. The main calls it on each heartbeat and on a ticker.
//
// This is the "умнее" version of the operator's `if metric()==old: pull` idea:
// instead of pulling when the metric is *unchanged*, we pull when the heard
// state has drifted from the pulled state in a way that matters, OR when the
// node just recovered, OR when a reconcile interval elapsed.
func ShouldPull(v NodeView, now time.Time, th Thresholds) PullDecision {
	th = th.withDefaults()

	// Never pulled yet but we have heard from it: get the baseline.
	if v.LastPull.IsZero() && !v.LastSeen.IsZero() {
		return PullDecision{true, "initial baseline pull"}
	}
	// Config changed since last pull (user added/removed/toggled): authoritative
	// config must be refreshed.
	if v.HeardConfigVersion > v.PulledConfigVersion {
		return PullDecision{true, "config_version advanced"}
	}
	// Enough new traffic accumulated to be worth reconciling per-user counters.
	if v.HeardTotalBytes-v.PulledTotalBytes >= th.ByteDelta {
		return PullDecision{true, "traffic delta over threshold"}
	}
	// Periodic safety-net reconcile.
	if now.Sub(v.LastPull) >= th.ReconcileEvery {
		return PullDecision{true, "reconcile interval elapsed"}
	}
	return PullDecision{false, ""}
}

// MarkDown reports whether a node should be considered DOWN given the last
// heartbeat time, the heartbeat interval and how many misses are tolerated.
func MarkDown(lastSeen time.Time, now time.Time, interval time.Duration, allowedMisses int) bool {
	if lastSeen.IsZero() {
		return false // never seen yet; not "down", just unknown
	}
	if allowedMisses <= 0 {
		allowedMisses = 3
	}
	if interval <= 0 {
		interval = 20 * time.Second
	}
	return now.Sub(lastSeen) > time.Duration(allowedMisses)*interval
}
