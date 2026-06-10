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

## Node user API (`role: standalone` or `child`)

Token-guarded by `Authorization: Bearer <api_token>` (the `api_token` generated
by `vlr init`; empty token disables these endpoints). This is the prod automation
surface — every field is optional, and creating a user auto-renders + reloads Xray.

### `POST /v1/users`
Create a user. Body (all optional): `{"telegram_id":9876567,"id":"cust-42","email":"","profile":"mobile"}`
— or even `{}`. Returns:
```json
{ "uuid":"...", "link":"vless://...#tg9876567", "subscription":"<base64>" }
```
```bash
curl -fsS -XPOST https://node1.example.com/v1/users \
  -H "Authorization: Bearer $TOKEN" -d '{"telegram_id":9876567}'
```

### `DELETE /v1/users/<ref>`
Delete by `uuid|email|id|telegram-id`. Auto-applies Xray.

### `GET /v1/users`
List users (token-guarded).

> Bind/expose: the node serves these on `child.pull_listen` (default
> `127.0.0.1:9777`). For public prod use, front it with TLS (the Reality :443 SNI
> router or a reverse proxy) — do not expose `:9777` raw.

### `GET /sub/<ref>`
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
