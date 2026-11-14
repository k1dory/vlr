package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/k1dory/vlr/internal/cascade"
	"github.com/k1dory/vlr/internal/config"
)

// cascadeGuardEvery is how often the cascade policy routing is re-checked.
const cascadeGuardEvery = 30 * time.Second

// startCascadeGuard keeps Xray's egress inside the cascade: it installs the
// systemd-networkd drop-in (so networkd stops flushing our rules) and restores
// the fwmark/oif rules and the tunnel route if they disappear anyway.
func startCascadeGuard(ctx context.Context, cfg *config.Config, log *slog.Logger) {
	if !cfg.Cascade.Enabled || cfg.Cascade.Interface == "" {
		return
	}
	if created, err := cascade.EnsureNetworkdKeepsRules(cascade.NetworkdDropIn); err != nil {
		log.Warn("could not write networkd drop-in; cascade rules may be flushed on networkd restart",
			"path", cascade.NetworkdDropIn, "err", err)
	} else if created {
		log.Info("networkd drop-in installed", "path", cascade.NetworkdDropIn)
	}
	go cascade.GuardRules(ctx, log, cfg.Cascade.Interface, config.CascadeFwmark, cascadeGuardEvery)
}
