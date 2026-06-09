---
description: vlr — VLESS+Reality cascade VPN node utility
alwaysApply: true
---

# vlr — Development Guide

`vlr` is one static Go binary that is, by role: a CLI, a VPN-node daemon, and a
main-server API. It runs a **VLESS+Reality(+Vision)** entry, cascades RU→EU over
**WireGuard**, and streams each client into a **base64 subscription link**.

Sibling projects this draws from: White_Rabbit (Go/SOLID/ACID, RU-entry/EU-exit),
infrashark-mtg (the proven `mtg kaskad` RU→EU pattern + global-command install),
FlyTelega (Aeza + Yandex Cloud API docs).

## Methodology: Documentation First → TDD → Review

1. Write the spec/doc in `docs/` before the code.
2. Pure decision logic gets a unit test first (`internal/protocol` is the model).
3. Self-review the diff for secrets/TODOs; `make vet test` must be green.

## Locked architectural decisions (do not silently revert)

- **Cascade transport = WireGuard.** Not SOCKS5/SSH (drops UDP, breaks HTTP/3),
  not Xray dialerProxy (double Reality, slower). Inner hop is DC↔DC → no camouflage.
- **Fingerprint default = `randomized`, never `chrome`.** Google SNIs are rejected
  in code (`reality.ValidateSNI`). See `docs/FINGERPRINT.md`.
- **Vision is per-profile.** `desktop` profile = plain VLESS+Reality (no `flow`);
  `mobile` = `xtls-rprx-vision`.
- **Entry on trusted RU cloud (Yandex), exit on cheap EU (Aeza).** RU mobile DPI
  blocks by hosting-IP reputation — Aeza ranges get blocked on mobile, so Aeza is
  the EU *exit*, never the RU entry. (Genomed-mtproto lesson.)
- **Child↔Main = push heartbeat + delta-triggered pull.** `protocol.ShouldPull`
  is pure and tested; tune via `main` config. Don't replace with constant polling.

## Code standards (SOLID, like White_Rabbit)

- Small interfaces (`CascadeMonitor`, `StatsPoller`) + constructor injection so
  daemons are testable and dev/Windows uses no-op impls.
- Always check errors; wrap with `fmt.Errorf("op: %w", err)`. Structured `slog`.
- **Zero external Go dependencies.** Reality x25519 = stdlib `crypto/ecdh`,
  config = JSON, CLI = stdlib `flag`. Keep it buildable on any RU/EU box offline.
- Config files hold private keys → written 0600, atomic (temp+rename).

## Commands

```
make build | test | vet | build-linux
./install.sh           # global `vlr` + systemd unit (like mtg's install.sh)
vlr init|keys|cascade|user|node|render|serve|status|version
```

## Layout

`cmd/vlr` entry · `internal/{reality,wireguard,xray,subscription,store,protocol,
daemon,cascade,config,util}` · `deploy/` · `docs/`.

## Not done yet (next iterations)

- Real `StatsPoller` against Xray StatsService gRPC (currently `cascade.NoopStats`).
- Heartbeat bearer verification on the main (`handleHeartbeat` TODO).
- PostgreSQL persistence on main (currently in-memory `detail` map).
- `vlr up/down` that actually exec wg-quick + restart Xray (currently `render` +
  `cascade gen` emit configs for the operator/systemd to apply).
- TG bot + web subscription frontend (per White_Rabbit design system).
