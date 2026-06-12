package cascade

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/k1dory/vlr/internal/store"
)

// XrayStats is the real per-user traffic poller. It shells out to the Xray CLI
// (`xray api statsquery`) against the local StatsService inbound, so it needs no
// gRPC/proto dependency — keeping the binary stdlib-only. Counters are cumulative
// since Xray start (no -reset), i.e. monotonic within a node lifetime, which is
// exactly what store.User.{Rx,Tx}Bytes expect.
type XrayStats struct {
	// APIAddr is the Xray StatsService address (the api-in inbound). Defaults to
	// 127.0.0.1:10085, matching the rendered config.
	APIAddr string
	// Bin is the xray binary name/path. Defaults to "xray".
	Bin string
}

type statsQuery struct {
	Stat []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"stat"`
}

// Poll queries Xray for all per-user counters and writes them into the store.
// Stat names have the form "user>>>{statID}>>>traffic>>>{uplink|downlink}", where
// statID is the inbound client email (xray.StatID: email or UUID).
func (x XrayStats) Poll(ctx context.Context, s *store.Store) error {
	addr := x.APIAddr
	if addr == "" {
		addr = "127.0.0.1:10085"
	}
	bin := x.Bin
	if bin == "" {
		bin = "xray"
	}
	out, err := exec.CommandContext(ctx, bin, "api", "statsquery",
		"--server="+addr, "-pattern", "user>>>").Output()
	if err != nil {
		return fmt.Errorf("xray statsquery: %w", err)
	}
	var q statsQuery
	if err := json.Unmarshal(out, &q); err != nil {
		return fmt.Errorf("parse stats: %w", err)
	}

	type counter struct{ rx, tx int64 }
	agg := map[string]*counter{}
	for _, st := range q.Stat {
		parts := strings.Split(st.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		id := parts[1]
		v, perr := strconv.ParseInt(st.Value, 10, 64)
		if perr != nil {
			continue
		}
		c := agg[id]
		if c == nil {
			c = &counter{}
			agg[id] = c
		}
		switch parts[3] {
		case "downlink": // server -> client = received by the user
			c.rx = v
		case "uplink": // client -> server = sent by the user
			c.tx = v
		}
	}

	var firstErr error
	for id, c := range agg {
		if err := s.UpdateTraffic(id, c.rx, c.tx); err != nil && firstErr == nil {
			firstErr = err // user may have just been removed; keep going
		}
	}
	return firstErr
}
