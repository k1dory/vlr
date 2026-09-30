# The RU→EU cascade (AmneziaWG)

`vlr cascade up` provisions **AmneziaWG (AWG)** on both ends. It generates
one set of obfuscation parameters and saves it in `cascade.awg`; subsequent
runs reuse it. Configs live in `/etc/amnezia/amneziawg`, and handshake checks
use `awg`. Existing configs without `cascade.transport` keep using WireGuard
until `cascade up` explicitly migrates them.

On a terminal the command asks whether to start the tunnel automatically after
reboot, even when `--eu-host` is supplied. `--autostart=true|false` bypasses that
question for automation. Both ends use the same setting; false brings the
tunnel up now but disables boot startup. The selection is saved in
`cascade.autostart`. Fresh scripted installations default to autostart.

Automatic installation uses the official Amnezia PPA on Ubuntu; on other
systems install `awg`, `awg-quick` and the AmneziaWG kernel module or
`amneziawg-go` beforehand. Reference:
https://github.com/amnezia-vpn/amneziawg-linux-kernel-module#installation


The EU config opens its AWG UDP listen port in INPUT and installs forwarding
rules before existing firewall chains. These rules are scoped to the port and
tunnel, are restored on interface startup, and removed on shutdown. Existing
SSH and web firewall rules are left in place.

Installation output is saved to a private `vlr-install-*.log` file; the CLI shows
its path and a bounded error excerpt on failure. Cascade setup waits up to
20 seconds for a handshake before probing sites, prints each probe result as
it finishes, and refreshes an active `vlr` daemon using the same config file.

## Transport

New cascades use AmneziaWG, retaining UDP/HTTP3 support and adding configurable
packet obfuscation. The implementation uses the basic Jc/Jmin/Jmax, S1/S2,
H1–H4 parameters; it does not configure the newer AWG 2/3 extensions.

## Topology

- **RU entry** routes the Xray `freedom` egress into `wg-cascade`. A policy route
  (`Table = off` + `fwmark`/`ip rule`) sends only client-egress traffic through
  the tunnel, keeping the node's own management traffic (SSH, heartbeat) on the
  default route.
- **EU exit** terminates the tunnel and NATs (`MASQUERADE`) out its WAN NIC.

```
[RU] wg-cascade 10.66.0.2/32  ──AmneziaWG UDP──►  [EU] wg-cascade 10.66.0.1/24
     AllowedIPs 0.0.0.0/0                             AllowedIPs 10.66.0.2/32
     default route into tunnel                        MASQUERADE -> eth0 -> Internet
```

## Bring up the cascade — one command from the RU node

You do **not** log into the EU box. From the **RU** node, `vlr cascade up` does the
whole thing (this mirrors Genomed-mtproto's `mtg kaskad`): it generates the RU
key, SSHes into EU (key or password), stands up a **forward-only** AmneziaWG exit
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
`--iface wg-cascade`, `--ru-ip 10.66.0.2`, `--eu-ip 10.66.0.1`, `--autostart=true|false`, `--no-check`.

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
vlr cascade test                               # just the WG/AWG handshake
```

### Manual / advanced fallback

`vlr cascade gen` prints the RU config with its saved transport parameters.
The legacy `vlr cascade exit --entry-pubkey <RU_PUB> --wan eth0` prints a plain
WG EU config. For AWG use the automated `vlr cascade up`.

## `cascade` config block

```json
"cascade": {
  "enabled": true,
  "transport": "awg",
  "autostart": true,
  "awg": {"jc": 4, "jmin": 8, "jmax": 80, "s1": 64, "s2": 32,
          "h1": 10001, "h2": 20002, "h3": 30003, "h4": 40004},
  "interface": "wg-cascade",
  "address": "10.66.0.2/32",
  "private_key": "<RU WG private>",
  "listen_port": 51820,
  "mtu": 1420,
  "exit_public_key": "<EU WG public>",
  "exit_endpoint": "aeza-exit.example:51820",
  "exit_allowed_ip": "0.0.0.0/0",
  "exit_tunnel_ip": "10.66.0.1",
  "keepalive": 25
}
```

### MTU & QUIC

Default MTU 1420. If you see QUIC/HTTP3 stalls behind a provider that adds
encapsulation, drop to 1380–1400. WireGuard rehandshakes ~every 2 min under
traffic; `vlr cascade test` treats a handshake within 3 min as healthy, and the
daemon's monitor uses the same check for heartbeat `cascade_up`.
