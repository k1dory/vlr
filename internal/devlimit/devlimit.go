// Package devlimit caps how many devices (distinct source IPs) one subscription
// may use at once on a node.
//
// Xray has no native per-user device limit, but (since 25.x) it tracks each
// user's online IPs (policy statsUserOnline + `xray api statsonlineiplist`) and
// accepts routing rules at runtime (RoutingService + `xray api adrules`). The
// limiter combines the two:
//
//   - every tick it reads the online IPs per user;
//   - per user, IPs are ranked by when vlr first saw them; the first Max stay
//     allowed, every later one is banned for Ban;
//   - a ban is a routing rule {user:[email], source:[ip]} -> "block", placed
//     before the node's static rules. It hits only that user from that IP — never
//     other users behind the same CGNAT address, unlike a firewall IP ban.
//
// "Device" therefore means "source IP": two phones behind one home NAT count as
// one, one phone hopping Wi-Fi->LTE briefly counts as two until Xray drops the
// old IP. Bans are short so a legitimate switch recovers on its own.
package devlimit

import (
	"sort"
	"time"
)

// Policy is the node-wide device limit.
type Policy struct {
	Max int           // allowed concurrent IPs per user; <=0 disables the limiter
	Ban time.Duration // how long an over-limit IP stays blocked
}

// Ban is one blocked (user, ip) pair.
type Ban struct {
	User  string // Xray client email (xray.StatID)
	IP    string
	Until time.Time
}

// Limiter holds the per-user first-seen order and active bans. It is not safe
// for concurrent use; the daemon drives it from a single goroutine.
type Limiter struct {
	policy    Policy
	firstSeen map[string]map[string]time.Time // user -> ip -> first seen
	bans      map[string]map[string]time.Time // user -> ip -> banned until
}

// New returns a limiter for the given policy.
func New(p Policy) *Limiter {
	if p.Ban <= 0 {
		p.Ban = 60 * time.Second
	}
	return &Limiter{
		policy:    p,
		firstSeen: map[string]map[string]time.Time{},
		bans:      map[string]map[string]time.Time{},
	}
}

// Step feeds one observation of online IPs per user and returns the full set of
// active bans plus whether that set changed since the previous Step.
//
// Pure apart from the limiter's own state: no I/O, so it is unit-tested.
func (l *Limiter) Step(now time.Time, online map[string][]string) (bans []Ban, changed bool) {
	// 1. Expire bans.
	for u, ips := range l.bans {
		for ip, until := range ips {
			if !now.Before(until) {
				delete(ips, ip)
				changed = true
			}
		}
		if len(ips) == 0 {
			delete(l.bans, u)
		}
	}

	// 2. Forget IPs Xray no longer reports, remember new ones.
	for u, ips := range l.firstSeen {
		if _, ok := online[u]; !ok {
			delete(l.firstSeen, u)
			continue
		}
		cur := toSet(online[u])
		for ip := range ips {
			if _, ok := cur[ip]; !ok {
				delete(ips, ip)
			}
		}
	}
	for u, ips := range online {
		seen := l.firstSeen[u]
		if seen == nil {
			seen = map[string]time.Time{}
			l.firstSeen[u] = seen
		}
		for _, ip := range ips {
			if _, ok := seen[ip]; !ok {
				seen[ip] = now
			}
		}
	}

	// 3. Per user: banned IPs don't take a slot; the oldest Max of the rest are
	//    allowed, the remainder get banned.
	if l.policy.Max > 0 {
		for u, seen := range l.firstSeen {
			cand := make([]string, 0, len(seen))
			for ip := range seen {
				if _, banned := l.bans[u][ip]; !banned {
					cand = append(cand, ip)
				}
			}
			if len(cand) <= l.policy.Max {
				continue
			}
			sort.Slice(cand, func(i, j int) bool {
				ti, tj := seen[cand[i]], seen[cand[j]]
				if !ti.Equal(tj) {
					return ti.Before(tj)
				}
				return cand[i] < cand[j] // deterministic tie-break
			})
			for _, ip := range cand[l.policy.Max:] {
				if l.bans[u] == nil {
					l.bans[u] = map[string]time.Time{}
				}
				l.bans[u][ip] = now.Add(l.policy.Ban)
				changed = true
			}
		}
	}

	return l.Bans(), changed
}

// Bans returns the active bans sorted by user, then IP.
func (l *Limiter) Bans() []Ban {
	var out []Ban
	for u, ips := range l.bans {
		for ip, until := range ips {
			out = append(out, Ban{User: u, IP: ip, Until: until})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].User != out[j].User {
			return out[i].User < out[j].User
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func toSet(in []string) map[string]struct{} {
	m := make(map[string]struct{}, len(in))
	for _, s := range in {
		m[s] = struct{}{}
	}
	return m
}
