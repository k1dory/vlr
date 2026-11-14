package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/k1dory/vlr/internal/cascade"
	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/wireguard"
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
	// Repairs are only safe when the tunnel's PostUp tolerates rules/route that
	// already exist; migrate hooks written by older vlr versions first.
	confPath := filepath.Join(cfg.Cascade.ConfigDir(), cfg.Cascade.Interface+".conf")
	changed, idempotent, err := wireguard.MigrateEntryHooks(confPath, config.CascadeFwmark)
	switch {
	case err != nil:
		log.Warn("could not check tunnel hooks; cascade guard will only report", "path", confPath, "err", err)
	case changed:
		log.Info("tunnel hooks migrated to idempotent form (applies on next tunnel start)", "path", confPath)
	case !idempotent:
		log.Warn("tunnel config has custom hooks; cascade guard will only report", "path", confPath)
	}
	repair := err == nil && idempotent
	unit := cfg.Cascade.Tool() + "-quick@" + cfg.Cascade.Interface
	go cascade.GuardRules(ctx, log, cfg.Cascade.Interface, unit, config.CascadeFwmark, cascadeGuardEvery, repair)
}
