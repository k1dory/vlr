# vlr Architecture

## Data path

```
        VLESS+Reality(+Vision), fp=randomized, :443
client  ───────────────────────────────────────────►  RU entry node
(v2rayNG/Hiddify)                                      (Yandex Cloud, whitelisted IP)
                                                          │  Xray terminates Reality,
                                                          │  freedom egress -> wg-cascade
                                                          │
                                                  WireGuard (kernel, UDP, HTTP/3-clean)
                                                          ▼
                                                       EU exit node
                                                       (Aeza) — NAT masquerade
                                                          │
                                                          ▼
                                                        Internet
```

The **outer hop** (client→RU) is the censored hop, so it uses Reality with a
randomized fingerprint and a reputable non-Google SNI. The **inner hop** (RU→EU)
is datacenter-to-datacenter — no DPI there — so it uses plain WireGuard for
maximum throughput and full UDP/QUIC support.

Why the entry sits on Yandex Cloud: RU mobile DPI (2026) blocks by **hosting IP
reputation**, not just handshake signature. Budget VPS ranges (incl. Aeza) get
their ranges blocked on mobile; a trusted RU cloud range passes as legitimate
business traffic. So entry = trusted RU cloud, exit = cheap EU (IP doesn't matter
once you're past the censor). This is the lesson carried over from the
Genomed-mtproto project.

## Control plane (child + main)

```
child node ──push heartbeat (cheap, ~20s)──►  main server
   ▲                                              │
   └──────── pull (heavy, on demand) ◄────────────┘
             triggered by delta logic
```

- Heartbeat: `{node_id, seq, healthy, cascade_up, user_count, config_version,
  total_bytes}` — constant size, always tells the main "alive + summary". A
  missing heartbeat = node DOWN (closes the pure-pull blind spot).
- Pull: `GET /v1/pull` returns full per-user accounting **plus the public Reality
  entry snapshot and each user's short id**, so the main can also rebuild every
  client's `vless://` link. The main calls it only when `protocol.ShouldPull`
  fires (see [MODES.md](MODES.md)).

The node binary's built-in `role: main` is an in-memory aggregator (zero-dep,
good for a single small fleet). The production control plane is a **separate
project, `vlr-main-agent`** (its own repo): it ingests the same heartbeat/pull
protocol but persists keys to **PostgreSQL** and traffic to **ClickHouse**,
issues subscriptions centrally, and hosts the Telegram subscription bot. Keeping
it out-of-tree is what lets this binary stay dependency-free.

## Components

| Package | Responsibility (Single Responsibility / SOLID) |
|---|---|
| `reality` | x25519 keys, short IDs, fingerprint + SNI policy |
| `wireguard` | WG keygen, RU-entry & EU-exit config render |
| `xray` | Xray config render (Reality+Vision inbound, stats, freedom egress) |
| `subscription` | one `vless://` link, base64 subscription stream |
| `store` | node-local state: users, per-user counters, config version |
| `protocol` | heartbeat/pull types + pure delta-pull decision (unit-tested) |
| `daemon` | standalone / child / main run loops |
| `cascade` | WG handshake health monitor, stats poller hook |
| `config` | node config load/save/validate |

Interfaces (`CascadeMonitor`, `StatsPoller`) keep the daemons testable and let
dev/Windows use no-op implementations while Linux nodes use the real `wg`-based
ones (Dependency Inversion).

## Methodology

Documentation First → TDD → Review, matching the White Rabbit project. The
load-bearing decision logic (`ShouldPull`, `MarkDown`) is pure and covered by
tests in `internal/protocol/protocol_test.go`.
