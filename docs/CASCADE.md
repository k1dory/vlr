# The RU→EU cascade (WireGuard)

## Why WireGuard

The inner hop carries *every* client byte after Reality is terminated, including
UDP and HTTP/3 (QUIC). Options compared:

| Transport | UDP / HTTP3 | Overhead | Verdict |
|---|---|---|---|
| SOCKS5 over SSH (mtg-style) | **breaks UDP/QUIC** | SSH crypto over Reality | rejected |
| Xray dialerProxy (VLESS+Reality inner) | UDP ok | double Reality, userspace | works, slower |
| **WireGuard** | **full UDP/QUIC** | kernel, minimal | **chosen** |

DC↔DC is not censored, so the inner hop needs no camouflage — pure speed wins.

## Topology

- **RU entry** routes the Xray `freedom` egress into `wg-cascade`. A policy route
  (`Table = off` + `fwmark`/`ip rule`) sends only client-egress traffic through
  the tunnel, keeping the node's own management traffic (SSH, heartbeat) on the
  default route.
- **EU exit** terminates the tunnel and NATs (`MASQUERADE`) out its WAN NIC.

```
[RU] wg-cascade 10.66.0.2/32  ──WireGuard UDP──►  [EU] wg-cascade 10.66.0.1/24
     AllowedIPs 0.0.0.0/0,::/0                        AllowedIPs 10.66.0.2/32
     default route into tunnel                        MASQUERADE -> eth0 -> Internet
```

## Bring up the cascade — one command from the RU node

You do **not** log into the EU box. From the **RU** node, `vlr cascade up` does the
whole thing (this mirrors Genomed-mtproto's `mtg kaskad`): it generates the RU
key, SSHes into EU (key or password), stands up a **forward-only** WireGuard exit
there (EU makes its own private key locally — it never leaves the box; only the
RU peer IP is allowed; NAT-only, no shell), wires both sides, brings the RU
interface up, and healthchecks through the tunnel.

```bash
# key auth
vlr cascade up --eu-host 5.6.7.8 --eu-user root --eu-key ~/.ssh/id_ed25519

# or password auth (needs sshpass on the RU node)
vlr cascade up --eu-host 5.6.7.8 --eu-user root --eu-pass 'secret'
```

Useful flags: `--wg-port 51820`, `--wan eth0` (EU NIC; empty = auto-detect),
`--iface wg-cascade`, `--ru-ip 10.66.0.2`, `--eu-ip 10.66.0.1`, `--no-check`.

Output ends with the reachability table:

```
==> проверка через каскад:
telegram.org            [OK]
amazon.com              [OK]
claude.ai               [OK]
openai.com              [OK]
notebooklm.google.com   [OK]
google.com              [OK]

✓ каскад RU→EU работает
```

Re-run the check any time:

```bash
vlr cascade check                              # built-in site list
vlr cascade check --sites ya.ru,github.com     # custom
vlr cascade test                               # just the WireGuard handshake
```

### Manual / advanced fallback

If you want to configure the two sides by hand (e.g. EU has no SSH from RU), the
old two-step still exists: `vlr cascade gen` prints the RU config, and
`vlr cascade exit --entry-pubkey <RU_PUB> --wan eth0` prints the EU config. The
automated `vlr cascade up` is the recommended path.

## `cascade` config block

```json
"cascade": {
  "enabled": true,
  "interface": "wg-cascade",
  "address": "10.66.0.2/32",
  "private_key": "<RU WG private>",
  "listen_port": 51820,
  "mtu": 1420,
  "exit_public_key": "<EU WG public>",
  "exit_endpoint": "aeza-exit.example:51820",
  "exit_allowed_ip": "0.0.0.0/0, ::/0",
  "exit_tunnel_ip": "10.66.0.1",
  "keepalive": 25
}
```

### MTU & QUIC

Default MTU 1420. If you see QUIC/HTTP3 stalls behind a provider that adds
encapsulation, drop to 1380–1400. WireGuard rehandshakes ~every 2 min under
traffic; `vlr cascade test` treats a handshake within 3 min as healthy, and the
daemon's monitor uses the same check for heartbeat `cascade_up`.
