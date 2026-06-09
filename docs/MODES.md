# Node modes & the child↔main protocol

## standalone

The node is self-contained:
- terminates Reality, runs the RU→EU cascade,
- stores users + counters in `<data_dir>/state.json`,
- serves its own base64 subscription on `/sub/<email>`,
- monitors the cascade (`wg show` handshake age) and Xray stats itself.

Use when you run a single node or want each node fully autonomous.

## child + main

Same data plane on the child, but state lives on the **main** server. Two message
types only:

### 1. Heartbeat (PUSH, child → main, ~every 20s)

Constant-size, cheap. Always sent:

```json
{ "node_id":"ru-yc-msk-01", "seq":42, "sent_unix":1749556800,
  "healthy":true, "cascade_up":true, "user_count":120,
  "config_version":17, "total_bytes":883400000000 }
```

The main records `last_seen`. If no heartbeat arrives for
`down_after_misses × interval`, the node is marked **DOWN** and alerted. This is
what a pure pull model can't do: distinguish "nothing changed" from "node/internet
is dead".

### 2. Pull (main → child, on demand)

`GET /v1/pull` (bearer-protected) returns heavy per-user detail:

```json
{ "node_id":"ru-yc-msk-01", "config_version":17,
  "users":[ {"email":"a@x","rx_bytes":...,"tx_bytes":...,"enabled":true}, ... ] }
```

### The delta-pull decision (`protocol.ShouldPull`)

This is the "smarter version" of `if metric()==old: pull`. Instead of pulling on
*unchanged* metrics, the main pulls when **heard state has drifted from pulled
state in a way that matters**:

```
pull if:
  never pulled but heard at least once   -> "initial baseline pull"
  heard config_version > pulled           -> "config_version advanced"
  heard total_bytes - pulled >= ByteDelta -> "traffic delta over threshold"
  now - last_pull >= ReconcileEvery       -> "reconcile interval elapsed"
else:
  do nothing
```

- `ByteDelta` (default 256 MiB): only reconcile per-user counters once enough new
  traffic accumulated. Avoids constant heavy polling.
- `ReconcileEvery` (default 10 min): safety net that re-syncs even if every other
  trigger was missed.
- Config changes (user add/remove/toggle) bump `config_version`, so the main
  pulls promptly when the access list changes — not on a slow timer.

The function is **pure** (no I/O), so it is deterministic and unit-tested. The
main calls it on every heartbeat and on a 5s ticker; a DOWN node is never pulled.

Tuning lives in `main` config:

```json
"main": { "api_listen":"0.0.0.0:8443", "down_after_misses":3,
          "pull_threshold":268435456, "reconcile_seconds":600 }
```

## Choosing a mode

| Want | Mode |
|---|---|
| One node, fully autonomous | standalone |
| Many nodes, central billing/monitoring, single subscription URL | child + main |
| Region survives main outage | child (keeps serving; main just loses fresh stats) |
