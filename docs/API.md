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
<child.pull_bearer>`. Returns `protocol.PullResponse` and is called only when
`protocol.ShouldPull` fires. The payload carries everything the main needs to
both account traffic **and** rebuild each client's `vless://` link centrally:

```json
{
  "node_id": "ru-yc-msk-01",
  "config_version": 17,
  "entry": {
    "host": "node1.example.com", "port": 443, "sni": "www.tinkoff.ru",
    "public_key": "<reality pubkey>", "fingerprint": "randomized",
    "short_ids": ["0a1b2c3d", "..."]
  },
  "users": [
    { "uuid": "...", "email": "a@x", "telegram_id": 9876567, "external_id": "",
      "profile": "vision", "short_id": "0a1b2c3d", "enabled": true,
      "rx_bytes": 12345, "tx_bytes": 67890 }
  ]
}
```

The `entry` block is **public** Reality material only (no server private key).
`short_id` + `entry` let `vlr-main-agent` reconstruct the exact link the node
issued. This is the data the external main agent persists (keys → PostgreSQL,
traffic → ClickHouse).

### `GET /healthz`
`200 {"cascade_up": <bool>}`.

## Node user API (`role: standalone` or `child`)

Token-guarded by `Authorization: Bearer <api_token>` (the `api_token` generated
by `vlr init`; empty token disables these endpoints). This is the prod automation
surface — every field is optional, and creating a user auto-renders + reloads Xray.

### `POST /v1/users`
Create a user. Body (all optional): `{"telegram_id":9876567,"id":"cust-42","email":"","profile":"vision"}`
— or even `{}`. `profile` is opt-in: `"vision"` = XTLS-Vision (mobile only),
empty/omitted = plain VLESS+Reality (works on every client). Returns:
```json
{ "uuid":"...", "link":"vless://...#tg9876567",
  "sub_url":"https://link.infrashark.tech/base64/9f3c1a7be04d82a6f1c05e7b2d4a8091",
  "subscription":"<base64>" }
```
`sub_url` is the ready-to-import public subscription URL — present only when
`sub_base_url` is set in the node config (`vlr init --sub-base-url ...`). It is
`<sub_base_url>/base64/<sub_token>`, where `sub_token` is a per-user opaque 128-bit
token: unguessable, carries no email/UUID, and can be rotated to revoke the link
without changing the VLESS credential (`vlr user rotate <ref>`).
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

### `GET /base64/<sub_token>`
**Standalone role only. This is the public subscription URL.** Returns the
**base64 subscription** for the user whose `sub_token` matches (import URL for
v2rayNG/Hiddify/NekoBox). Sets `Profile-Title` and `Subscription-Userinfo`
(`upload=<tx>; download=<rx>`) headers. The token is opaque and unguessable, so
this path is safe to expose publicly — front it with TLS as
`https://link.infrashark.tech/base64/<token>` (see
`deploy/caddy/link.infrashark.tech.Caddyfile`). Unknown/disabled → `404`.

### `GET /sub/<email>`
**Standalone role only. Legacy — prefer `/base64/<token>`.** Same body, keyed by
`email` instead of a token. Kept for backwards compatibility; email in the path is
enumerable and leaks into proxy logs, so do not expose it publicly. A user created
without an email has no `/sub` path.

### `POST /v1/authentik/event`  (auto-revoke webhook)
**Standalone role only. Enabled only when `authentik.enabled` and a
`webhook_secret` are set.** Event-driven revoke: Authentik's notification rule
POSTs here the instant a user is deactivated or deleted, and the node removes that
portal-provisioned user (`source: authentik`) and reloads Xray. Guarded by
`Authorization: Bearer <webhook_secret>`. Body (shaped by the Authentik body
mapping): `{"action":"model_updated"|"model_deleted","email":"...","is_active":false}`.
Returns `200 {"ok":true,"revoked":<bool>}` for any well-formed request (even
unknown users — so Authentik doesn't retry-storm); `401` on a bad secret. Only
`source: authentik` users are ever touched. See `docs/AUTHENTIK.md` §4.

### `GET /me`  (subscription portal)
**Standalone role only. Enabled only when `sub_base_url` is set.** The
self-service portal that sits behind an Authentik forward-auth proxy. Reads the
authenticated email from `portal_header` (default `X-Authentik-Email`), finds the
matching user — **auto-provisioning one on first visit** (create + apply Xray) —
and returns an HTML page with that user's personal subscription URL + Hiddify
deep-link. No header / non-email value → `403` (and no user is created). Also
served at `/` so the fronting vhost can proxy its root here. Per-user identity
comes from the session, not the URL: see `docs/AUTHENTIK.md` and
`deploy/caddy/sub.genomed-security.ru.Caddyfile`.

### `GET /healthz`
`200 {"node":..., "cascade_up":..., "users":...}`.

## Auth model

- **Heartbeat**: per-node JWT/bearer (`child.token`), issued by main on `vlr node
  register` in a full deployment.
- **Pull**: per-node bearer (`child.pull_bearer`) the main stores in its node
  registry (`vlr node register --bearer ...`). The child rejects pulls without it.
- Never expose the pull API to the public internet — bind it to the management
  network or tunnel it (ssh -L / WireGuard) as in the mtg deployment.
