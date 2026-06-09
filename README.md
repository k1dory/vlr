# vlr — VLESS+Reality cascade VPN node utility

`vlr` is a single static Go binary that runs a VPN node built on **VLESS + Reality
+ XTLS-Vision**, cascading traffic from a **RU entry** (Yandex Cloud) to an **EU
exit** (Aeza) over **WireGuard**, and streaming each client's access config into
a **base64 subscription link**.

One binary, three roles (chosen by the config file):

| Role | What it does |
|---|---|
| **standalone** | Owns everything: terminates Reality, runs the RU→EU cascade, stores users/metrics locally, monitors itself, serves its own base64 subscription. Nothing leaves the box. |
| **child** | Same data plane, but reports to a main server: **pushes** cheap heartbeats and **exposes** a pull API the main calls on demand. |
| **main** | No data plane. Ingests heartbeats, runs **delta-triggered pulls**, aggregates every child, issues subscriptions centrally. |

## Why these choices

- **Cascade = WireGuard, not SOCKS5/SSH.** The inner RU→EU hop carries *all*
  client traffic incl. UDP and **HTTP/3 (QUIC)**. A TCP SOCKS5 proxy drops UDP
  and breaks QUIC; SSH adds a second crypto layer over Reality. WireGuard is
  kernel-space, UDP-native, lowest inter-DC overhead. DC↔DC needs no camouflage.
- **Fingerprint = `randomized`, never `chrome`.** In RU (mid-2026) `chrome` +
  Google SNI is reset on sight; JA4+ now matches static fingerprints. `vlr`
  defaults to a randomized ClientHello (no stable signature) and **refuses Google
  SNIs**. See [docs/FINGERPRINT.md](docs/FINGERPRINT.md).
- **Vision is per-profile.** XTLS-Vision regresses on desktop, so `--profile
  desktop` users get plain VLESS+Reality (no `flow`); `mobile` keeps Vision.
- **Child↔Main = push heartbeat + delta-triggered pull.** Cheap constant-size
  heartbeat = liveness (a gap = node/internet down). Heavy per-user detail is
  pulled only when traffic crosses a threshold, config changes, a node recovers,
  or a reconcile interval elapses. Never hammers the node, never loses state.
  See [docs/MODES.md](docs/MODES.md).

## Quick start (standalone)

```bash
./install.sh                  # build + install global `vlr` + systemd unit

vlr init --role standalone --node-id ru-yc-msk-01   # public IP auto-detected
vlr cascade gen > /etc/wireguard/wg-cascade.conf   # RU side
# (on the EU box) vlr cascade exit --entry-pubkey <RU_WG_PUB> --wan eth0
wg-quick up wg-cascade

vlr user add --email you@example.com --telegram-id 123456789   # prints a vless:// share link
vlr user link --email you@example.com  # share link + base64 subscription
vlr render > /usr/local/etc/xray/config.json && systemctl restart xray
systemctl enable --now vlr             # node daemon (monitor + subscription)
```

## Quick start (child + main)

```bash
# main server (EU, no VPN data plane)
vlr init --role main --node-id main-eu --api-listen 0.0.0.0:8443
vlr node register --node-id ru-yc-msk-01 \
    --pull-url https://ru-node:9777/v1/pull --bearer <PULL_TOKEN>
vlr serve     # ingests heartbeats, schedules delta-pulls

# child node (RU)
vlr init --role child --node-id ru-yc-msk-01 --host 93.77.160.10 \
    --main-url https://main-eu:8443/v1
# set child.token / child.pull_bearer in the config, then:
vlr serve     # pushes heartbeats, exposes pull API
```

## Commands

```
vlr init        provision this node (--role standalone|child|main)
vlr keys        generate Reality / WireGuard keys (--type reality|wireguard)
vlr cascade     gen | exit | test   (RU->EU WireGuard hop)
vlr user        add | rm | list | link
vlr node        register | list      (main role)
vlr render      print the Xray config for this node
vlr serve       run the daemon for this node's role
vlr status      show node status
vlr version
```

## Build / test

```bash
make build     # static, CGO_ENABLED=0
make test      # unit tests (delta-pull logic is covered)
make vet
make build-linux   # cross-compile for a RU/EU node
```

Zero external dependencies — Reality x25519 keys come from the Go stdlib
(`crypto/ecdh`), so the binary builds on any box without a module proxy.

## Layout

```
cmd/vlr/                 CLI + daemon entrypoint
internal/reality/        x25519 keys, short IDs, fingerprint/SNI policy
internal/wireguard/      WG keygen + RU entry / EU exit config render
internal/xray/           Xray config render (Reality+Vision inbound, freedom egress)
internal/subscription/   vless:// link + base64 subscription stream
internal/store/          node-local JSON state (users, counters, config version)
internal/protocol/       heartbeat / pull wire types + delta-pull decision (tested)
internal/daemon/         standalone / child / main daemons
internal/cascade/        WireGuard handshake monitor + stats poller hook
internal/config/         node config (JSON) load/save/validate
deploy/                  systemd unit, compose
docs/                    architecture, cascade, modes, fingerprint, API
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full picture.
