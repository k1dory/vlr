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

## Generate the configs

On the **RU** node (after `vlr init` and filling `cascade` keys/endpoint):

```bash
vlr cascade gen > /etc/wireguard/wg-cascade.conf
```

On the **EU** node:

```bash
vlr cascade exit \
    --entry-pubkey <RU_WG_PUBLIC_KEY> \
    --entry-ip 10.66.0.2/32 \
    --listen 51820 --wan eth0 \
    > /etc/wireguard/wg-cascade.conf
# prints the EU public key -> put it into the RU config's exit_public_key
```

Bring up both:

```bash
wg-quick up wg-cascade
vlr cascade test       # PASS if there is a fresh WireGuard handshake
```

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
