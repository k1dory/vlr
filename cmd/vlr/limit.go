package main

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/k1dory/vlr/internal/config"
	"github.com/k1dory/vlr/internal/devlimit"
	"github.com/k1dory/vlr/internal/store"
)

// cmdLimit manages the per-subscription device limit (limits.max_devices).
//
//	vlr limit set --max-devices 3 [--ban-seconds 60] [--exempt a@x,b@y]
//	vlr limit off
//	vlr limit show            # policy + online IPs per user right now
func cmdLimit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vlr limit set --max-devices N [--ban-seconds S] | off | show")
	}
	sub, rest := args[0], args[1:]
	fs := newFlagSet("limit")
	cfgPath := fs.String("config", "", "config path")
	maxDev := fs.Int("max-devices", 0, "simultaneous devices (source IPs) per user; 0 = off")
	banSec := fs.Int("ban-seconds", 0, "how long an over-limit IP stays blocked (default 60)")
	checkSec := fs.Int("check-seconds", 0, "enforcement interval (default 15)")
	exempt := fs.String("exempt", "", "comma-separated user emails the limit never applies to (replaces the list; \"-\" clears)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	path := *cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	c, err := loadCfg(path)
	if err != nil {
		return err
	}

	switch sub {
	case "show":
		return showLimit(c)
	case "set", "off":
		if sub == "off" {
			c.Limits = config.LimitsConfig{}
		} else {
			if *maxDev <= 0 {
				return fmt.Errorf("--max-devices must be > 0 (use `vlr limit off` to disable)")
			}
			c.Limits.MaxDevices = *maxDev
			if *banSec > 0 {
				c.Limits.BanSeconds = *banSec
			}
			if *checkSec > 0 {
				c.Limits.CheckSeconds = *checkSec
			}
			switch *exempt {
			case "":
			case "-":
				c.Limits.Exempt = nil
			default:
				c.Limits.Exempt = dedupNonEmpty(strings.Split(*exempt, ","))
			}
		}
		if err := c.Validate(); err != nil {
			return err
		}
		if err := config.Save(path, c); err != nil {
			return err
		}
		// Re-render once so Xray exposes online IPs + the routing API, then
		// restart the daemon so it picks up the new policy.
		if st, err := store.Open(dataDir(c)); err == nil {
			applyXray(c, st, false)
		}
		if exec.Command("systemctl", "is-active", "--quiet", "vlr").Run() == nil {
			if err := exec.Command("systemctl", "restart", "vlr").Run(); err != nil {
				return fmt.Errorf("restart vlr: %w", err)
			}
		}
		if sub == "off" {
			fmt.Println("ок: лимит устройств выключен")
		} else {
			fmt.Printf("ок: не больше %d устройств (IP) на подписку; лишние блокируются на %ds\n",
				c.Limits.MaxDevices, intOr(c.Limits.BanSeconds, 60))
		}
		return nil
	default:
		return fmt.Errorf("unknown limit subcommand %q", sub)
	}
}

func showLimit(c *config.Config) error {
	if c.Limits.MaxDevices > 0 {
		fmt.Printf("лимит: %d устройств на подписку, бан %ds, проверка каждые %ds\n",
			c.Limits.MaxDevices, intOr(c.Limits.BanSeconds, 60), intOr(c.Limits.CheckSeconds, 15))
	} else {
		fmt.Println("лимит: выключен")
	}
	if len(c.Limits.Exempt) > 0 {
		fmt.Printf("без лимита: %s\n", strings.Join(c.Limits.Exempt, ", "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	online, err := devlimit.Xray{ConfigPath: c.Xray.ConfigPath}.Online(ctx)
	if err != nil {
		return fmt.Errorf("онлайн-IP недоступны (Xray запущен? конфиг отрендерен этой версией?): %w", err)
	}
	users := make([]string, 0, len(online))
	for u := range online {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return len(online[users[i]]) > len(online[users[j]]) })
	fmt.Printf("онлайн: %d пользователей\n", len(users))
	for _, u := range users {
		mark := ""
		if slices.Contains(c.Limits.Exempt, u) {
			mark = "  (без лимита)"
		} else if c.Limits.MaxDevices > 0 && len(online[u]) > c.Limits.MaxDevices {
			mark = "  ← сверх лимита"
		}
		fmt.Printf("  %-40s %d IP %v%s\n", u, len(online[u]), online[u], mark)
	}
	return nil
}

func intOr(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}
