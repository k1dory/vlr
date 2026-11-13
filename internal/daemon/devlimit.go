package daemon

import (
	"context"
	"log/slog"
	"os/exec"
	"time"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/devlimit"
)

// startDeviceLimit runs the per-user device limiter in the background when the
// node has limits.max_devices > 0 and an Xray binary to talk to.
//
// Runtime ban rules live only in Xray and the previous daemon's memory, so on
// every start (limit on or off) they are cleared first: `vlr limit off`, a raised
// limit or a new exemption must not leave bans behind.
func startDeviceLimit(ctx context.Context, cfg *config.Config, log *slog.Logger) {
	l := cfg.Limits
	if _, err := exec.LookPath("xray"); err != nil {
		if l.MaxDevices > 0 {
			log.Warn("device limit configured but xray not found; not enforcing", "max_devices", l.MaxDevices)
		}
		return
	}
	x := devlimit.Xray{ConfigPath: cfg.Xray.ConfigPath}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := x.ApplyBans(cctx, nil); err != nil {
		log.Warn("device limit: clearing stale bans failed", "err", err)
	}
	cancel()
	if l.MaxDevices <= 0 {
		return
	}
	p := devlimit.Policy{Max: l.MaxDevices, Ban: time.Duration(l.BanSeconds) * time.Second, Exempt: l.Exempt}
	go devlimit.Run(ctx, log, p, x, time.Duration(l.CheckSeconds)*time.Second)
}
