# Device limit (`limits.max_devices`)

Caps how many devices one subscription may use **at the same time on a node**.
A device = a distinct client source IP that Xray currently sees for that user.

```bash
vlr limit set --max-devices 3            # 3 devices per subscription (ban 60s, check 15s)
vlr limit set --max-devices 2 --ban-seconds 120
vlr limit show                           # policy + online IPs per user right now
vlr limit off
```

`limit set` saves `/etc/vlr/config.json`, re-renders Xray once and restarts the
`vlr` daemon. Requires **Xray ≥ 25.x** (`statsonlineiplist`).

## How it works

- Xray tracks online IPs per user (`policy.levels.0.statsUserOnline`), read
  every `check_seconds` via `xray api statsonlineiplist -all`.
- Per user, IPs are ordered by when vlr first saw them. The first
  `max_devices` are allowed; every newer one is blocked for `ban_seconds`.
- A ban is a runtime routing rule `{user:[email], source:[ip]} → block`, pushed
  with `xray api adrules` (RoutingService) **in front of** the static rules taken
  from the live Xray config. No Xray restart; it hits only that user from that IP,
  so other users behind the same CGNAT address are unaffected.
- Bans are re-pushed every tick while any exist (they survive an Xray restart
  after `vlr user add`); when the last ban lifts the static rules are restored.

## Limits of the approach

- Devices behind one NAT share an IP and count as one.
- A phone switching Wi-Fi → LTE counts as two while Xray still lists the old IP;
  the new IP is blocked for `ban_seconds`, then recovers once the old one drops.
  Keep the ban short.
- Already-established connections of a banned IP are not cut; new ones are.
- The limit is per node. With two nodes a user can be on both at once.
