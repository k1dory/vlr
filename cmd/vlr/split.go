package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/k1dory/vlr/internal/config"
)

// dedupNonEmpty trims, drops empties and removes duplicates, preserving order.
func dedupNonEmpty(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// cmdSplit manages the split-tunnel RU-direct lists. Domains here egress directly
// from the RU node instead of cascading to EU.
func cmdSplit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vlr split add|rm|list [--domain x | --geosite y]")
	}
	sub, rest := args[0], args[1:]
	fs := newFlagSet("split")
	cfgPath := fs.String("config", "", "config path")
	domain := fs.String("domain", "", "domain matcher (bare host or full:/domain:/regexp:)")
	geosite := fs.String("geosite", "", "Xray geosite group, e.g. category-ru")
	_ = fs.Parse(rest)

	path := *cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	c, err := loadCfg(path)
	if err != nil {
		return err
	}

	switch sub {
	case "list":
		fmt.Println("RU-direct (выходят напрямую с RU, не через EU):")
		for _, d := range c.Split.RUDirect {
			fmt.Println("  domain:", d)
		}
		for _, g := range c.Split.GeositeRU {
			fmt.Println("  geosite:", g)
		}
		if len(c.Split.RUDirect) == 0 && len(c.Split.GeositeRU) == 0 {
			fmt.Println("  (пусто — весь трафик идёт RU→EU)")
		}
		return nil

	case "add":
		if *domain == "" && *geosite == "" {
			return fmt.Errorf("--domain или --geosite обязателен")
		}
		if *domain != "" {
			c.Split.RUDirect = dedupNonEmpty(append(c.Split.RUDirect, *domain))
		}
		if *geosite != "" {
			c.Split.GeositeRU = dedupNonEmpty(append(c.Split.GeositeRU, *geosite))
		}
	case "rm":
		if *domain != "" {
			c.Split.RUDirect = slices.DeleteFunc(c.Split.RUDirect, func(s string) bool { return s == *domain })
		}
		if *geosite != "" {
			c.Split.GeositeRU = slices.DeleteFunc(c.Split.GeositeRU, func(s string) bool { return s == *geosite })
		}
	default:
		return fmt.Errorf("unknown split subcommand %q", sub)
	}

	if err := config.Save(path, c); err != nil {
		return err
	}
	fmt.Println("ок. применить: vlr render > /usr/local/etc/xray/config.json && systemctl restart xray")
	return nil
}
