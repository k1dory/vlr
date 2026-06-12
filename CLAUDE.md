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
- **Vision is opt-in per user.** Default (empty profile) = plain VLESS+Reality
  (no `flow`, works on every client); `profile=vision` = `xtls-rprx-vision`
  (mobile only). Xray pins the flow per UUID, so one credential can't be both.
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
vlr init|keys|cascade|user|split|node|up|render|serve|status|uninstall|ledger|version
```

## Layout

`cmd/vlr` entry · `internal/{reality,wireguard,xray,subscription,store,protocol,
daemon,cascade,config,util}` · `deploy/` · `docs/`.

## Not done yet (next iterations)

- Heartbeat bearer verification on the **in-tree** `role: main` (`handleHeartbeat`
  TODO) — it accepts any well-formed heartbeat, so it is dev / single-fleet only.
  The production control plane (`vlr-main-agent`, separate repo) verifies the
  per-node heartbeat token and persists to PostgreSQL + ClickHouse.
- `vlr down` (the symmetric teardown of `vlr up`). `vlr up` is done: it installs
  xray-core if missing, grants it `CAP_NET_ADMIN`, renders the config and restarts
  Xray with a self-diagnosis. The cascade is automated too (`vlr cascade up`).
- TG subscription-gate bot + web subscription frontend (the bot is a separate
  component of `vlr-main-agent`).

Done since first draft: per-user traffic stats are real now — `cascade.XrayStats`
shells out to `xray api statsquery` (no gRPC dep) and is auto-selected when the
xray binary is present (`pickStats`). Per-user identity in the Xray config is
`xray.StatID` (email, else UUID) so users without an email still get accounted.

## Declarative install/uninstall (ledger)

Everything install touches is recorded append-only in `/etc/vlr/ledger.jsonl`
(`internal/ledger`): install.sh records binary/unit/enable/go-toolchain (Go only
if it installed it), `vlr init` records config dir/file, `vlr cascade up` records
the local WG iface + EU exit (host/user/port/key — never the password).
`vlr uninstall` reverses it in safe order (stop daemon → EU teardown → local wg →
unit → go/pkgs opt-in → binary → /etc/vlr last) and is idempotent. It also works
without the ledger via a declarative fallback derived from the config. Flags:
`--yes --keep-config --skip-eu --remove-go --remove-packages --eu-key/--eu-pass`.
Go/wireguard removed ONLY if vlr installed them AND opt-in. `cmd/vlr/uninstall.go`.

## Cascade automation (vlr cascade up)

`vlr cascade up --eu-host <ip> --eu-key|--eu-pass` runs from the RU node and
provisions the EU exit over system SSH (sshpass for passwords) — no Go SSH dep.
EU generates its own WG private key locally (never leaves the box), accepts only
the RU peer IP, NAT-only (forward-only, the WG analogue of mtg's restricted SSH
key). Ends with a curl-through-`--interface wg-cascade` `[OK]/[FAIL]` healthcheck
(`internal/cascade/provision.go`; `BuildExitScript`/`FormatResults` are unit-tested).
`vlr cascade check` re-runs the site probe; `vlr cascade test` checks the handshake.
