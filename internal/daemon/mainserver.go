package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/protocol"
)

// NodeReg is how the main reaches a child to pull from it.
type NodeReg struct {
	NodeID     string `json:"node_id"`
	PullURL    string `json:"pull_url"`    // https://child:9777/v1/pull
	PullBearer string `json:"pull_bearer"` // token the child requires
}

// MainServer aggregates every child: it ingests heartbeats, decides when to pull
// (protocol.ShouldPull), performs the pull, and tracks DOWN nodes.
type MainServer struct {
	cfg  *config.Config
	log  *slog.Logger
	http *http.Client

	mu    sync.Mutex
	views map[string]*protocol.NodeView // node_id -> running state
	regs  map[string]NodeReg            // node_id -> pull endpoint
	// detail holds the last pulled PullResponse per node (the authoritative data
	// the main would otherwise persist in PostgreSQL).
	detail map[string]protocol.PullResponse
}

// NewMainServer wires the main server. regs seeds known children (from a
// registry file); children may also auto-register on first heartbeat if allowed.
func NewMainServer(cfg *config.Config, log *slog.Logger, regs []NodeReg) *MainServer {
	m := &MainServer{
		cfg:    cfg,
		log:    log,
		http:   &http.Client{Timeout: 15 * time.Second},
		views:  map[string]*protocol.NodeView{},
		regs:   map[string]NodeReg{},
		detail: map[string]protocol.PullResponse{},
	}
	for _, r := range regs {
		m.regs[r.NodeID] = r
	}
	return m
}

// Run starts the HTTP API and the pull/health scheduler.
func (m *MainServer) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/heartbeat", m.handleHeartbeat)
	mux.HandleFunc("/v1/nodes", m.handleNodes)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	srv := &http.Server{Addr: m.cfg.Main.APIListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go m.scheduler(ctx)
	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sd)
	}()

	m.log.Info("main up", "listen", m.cfg.Main.APIListen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("main api: %w", err)
	}
	return nil
}

// handleHeartbeat ingests a child heartbeat and updates its view.
func (m *MainServer) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	var hb protocol.Heartbeat
	if err := json.Unmarshal(body, &hb); err != nil || hb.NodeID == "" {
		http.Error(w, "bad heartbeat", http.StatusBadRequest)
		return
	}
	// NOTE: production must verify Bearer == issued node token here.

	m.mu.Lock()
	v := m.views[hb.NodeID]
	if v == nil {
		v = &protocol.NodeView{NodeID: hb.NodeID}
		m.views[hb.NodeID] = v
	}
	v.LastSeen = time.Now()
	v.LastSeq = hb.Seq
	v.Healthy = hb.Healthy
	v.CascadeUp = hb.CascadeUp
	v.Down = false
	v.HeardConfigVersion = hb.ConfigVersion
	v.HeardTotalBytes = hb.TotalBytes
	m.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNodes exposes the aggregated view (for the operator / web UI).
func (m *MainServer) handleNodes(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.NodeView, 0, len(m.views))
	for _, v := range m.views {
		out = append(out, *v)
	}
	writeJSON(w, http.StatusOK, out)
}

// scheduler runs the down-detection and delta-pull loop.
func (m *MainServer) scheduler(ctx context.Context) {
	th := protocol.Thresholds{
		ByteDelta:      m.cfg.Main.PullThreshold,
		ReconcileEvery: time.Duration(m.cfg.Main.ReconcileSeconds) * time.Second,
	}
	hbInterval := 20 * time.Second
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			m.mu.Lock()
			var toPull []string
			for id, v := range m.views {
				// down detection
				if protocol.MarkDown(v.LastSeen, now, hbInterval, m.cfg.Main.DownAfterMisses) {
					if !v.Down {
						v.Down = true
						m.log.Warn("node DOWN", "node", id, "last_seen", v.LastSeen)
					}
					continue // don't try to pull a dead node
				}
				if d := protocol.ShouldPull(*v, now, th); d.Pull {
					m.log.Info("pull scheduled", "node", id, "reason", d.Reason)
					toPull = append(toPull, id)
				}
			}
			m.mu.Unlock()

			for _, id := range toPull {
				m.pull(ctx, id)
			}
		}
	}
}

// pull fetches heavy detail from a child and commits it as authoritative.
func (m *MainServer) pull(ctx context.Context, nodeID string) {
	m.mu.Lock()
	reg, ok := m.regs[nodeID]
	m.mu.Unlock()
	if !ok {
		m.log.Warn("no pull registration for node", "node", nodeID)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reg.PullURL, nil)
	if err != nil {
		m.log.Warn("pull build failed", "node", nodeID, "err", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+reg.PullBearer)
	resp, err := m.http.Do(req)
	if err != nil {
		m.log.Warn("pull failed", "node", nodeID, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.log.Warn("pull bad status", "node", nodeID, "status", resp.StatusCode)
		return
	}
	var pr protocol.PullResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		m.log.Warn("pull decode failed", "node", nodeID, "err", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.detail[nodeID] = pr
	if v := m.views[nodeID]; v != nil {
		v.PulledConfigVersion = pr.ConfigVersion
		var total int64
		for _, u := range pr.Users {
			total += u.RxBytes + u.TxBytes
		}
		v.PulledTotalBytes = total
		v.LastPull = time.Now()
	}
	m.log.Info("pull committed", "node", nodeID, "users", len(pr.Users), "cfgv", pr.ConfigVersion)
	// In production: UPSERT pr into PostgreSQL here.
}

// Register adds/updates a child's pull endpoint (called from the CLI/API).
func (m *MainServer) Register(reg NodeReg) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.regs[reg.NodeID] = reg
}
