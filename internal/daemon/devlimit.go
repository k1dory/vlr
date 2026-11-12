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
func startDeviceLimit(ctx context.Context, cfg *config.Config, log *slog.Logger) {
	l := cfg.Limits
	if l.MaxDevices <= 0 {
		return
	}
	if _, err := exec.LookPath("xray"); err != nil {
		log.Warn("device limit configured but xray not found; not enforcing", "max_devices", l.MaxDevices)
		return
	}
	p := devlimit.Policy{Max: l.MaxDevices, Ban: time.Duration(l.BanSeconds) * time.Second}
	x := devlimit.Xray{ConfigPath: cfg.Xray.ConfigPath}
	go devlimit.Run(ctx, log, p, x, time.Duration(l.CheckSeconds)*time.Second)
}
