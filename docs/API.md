# vlr HTTP API

Two surfaces: the **main** server API (heartbeat ingest + aggregation) and the
**child** pull API. Both are JSON over HTTP; put them behind TLS in production
(reverse proxy or the entry node's :443 SNI router).

## Main server (`role: main`, `main.api_listen`)

### `POST /v1/heartbeat`
Child → main liveness + summary push. Bearer = node token.

Request body: `protocol.Heartbeat`
```json
{ "node_id":"ru-yc-msk-01", "seq":42, "sent_unix":1749556800,
  "healthy":true, "cascade_up":true, "user_count":120,
  "config_version":17, "total_bytes":883400000000 }
```
Response: `200 {"ok":true}`. Bad body → `400`.

> TODO (production): verify `Authorization: Bearer <node token>` against the
> issued token. The scaffold accepts any well-formed heartbeat.

### `GET /v1/nodes`
Operator/web view — array of `protocol.NodeView` (last_seen, healthy, down,
heard vs pulled config_version & bytes, last_pull).

### `GET /healthz`
`200 {"ok":true}`.

## Child node (`role: child`, `child.pull_listen`)

### `GET /v1/pull`
Main → child heavy detail fetch. Requires `Authorization: Bearer
<child.pull_bearer>`. Returns `protocol.PullResponse` (per-user accounting +
config version). Called only when `protocol.ShouldPull` fires.

### `GET /healthz`
`200 {"cascade_up": <bool>}`.

## Standalone node (`role: standalone`)

### `GET /sub/<email>`
Returns the **base64 subscription** for that user (import URL for
v2rayNG/Hiddify/NekoBox). Sets a `Profile-Title` header.

### `GET /healthz`
`200 {"node":..., "cascade_up":..., "users":...}`.

## Auth model

- **Heartbeat**: per-node JWT/bearer (`child.token`), issued by main on `vlr node
  register` in a full deployment.
- **Pull**: per-node bearer (`child.pull_bearer`) the main stores in its node
  registry (`vlr node register --bearer ...`). The child rejects pulls without it.
- Never expose the pull API to the public internet — bind it to the management
  network or tunnel it (ssh -L / WireGuard) as in the mtg deployment.
