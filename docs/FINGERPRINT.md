# Fingerprint & SNI policy

## The problem (RU DPI, mid-2026)

- The default uTLS **`chrome`** fingerprint paired with a **Google SNI**
  (`www.google.com`, `youtube.com`, …) is **reset on sight**: the ClientHello
  signature is in the DPI blocklist.
- uTLS **< 1.8.2** ships fingerprintable hellos (`HelloChrome_120..133`); each is
  individually matchable.
- **JA4+** is now used by the big CDNs and increasingly by state DPI, combined
  with behavioural signals. A *static* fingerprint is a stable JA4 — i.e. a
  pattern waiting to be blocked.

## What vlr does

1. **Default fingerprint = `randomized`** (`reality.DefaultFingerprint`). Every
   dial draws a fresh ClientHello → no stable JA4 to block. Set in `vlr init` and
   emitted into both the Xray inbound expectation and the `fp=` of every
   `vless://` link.
2. **Google SNIs are rejected.** `reality.ValidateSNI` refuses
   `www.google.com`, `youtube.com`, `gstatic.com`, etc. `vlr init` fails if you
   pass one.
3. **Recommended SNIs** pair with a trusted RU-cloud entry IP and resolve to real
   TLS1.3 hosts: `storage.yandexcloud.net` (matches a Yandex Cloud range — no
   SNI↔IP mismatch), `www.tinkoff.ru`, `www.wildberries.ru`, `cdn-static.ozone.ru`.
   **An own domain on the entry IP beats all of these** (zero mismatch). When you
   have one, set `entry.sni` + `entry.dest` to it.
4. **Risky fingerprints warn.** `reality.ValidateFingerprint("chrome")` returns a
   warning; `ios` and `qq` are the best static fallbacks for RU survivability if a
   particular client dislikes the randomized hello.

## Keep uTLS current

Reality fingerprints come from the Xray-core build, not from `vlr`. On the node:

```bash
xray version          # check the bundled uTLS is >= 1.8.2 era (Xray 1.8.13+)
```

Upgrade Xray-core regularly; an old uTLS undoes the randomization benefit.

## Operational rules

- Rotate `short_ids` per client tier (vlr generates 3 by default and spreads
  users across them) so one leaked client config can be revoked without rotating
  the inbound.
- Keep the entry on a **trusted RU cloud** range. Per the Genomed-mtproto finding,
  RU mobile DPI blocks by **hosting IP reputation** — a budget VPS range (incl.
  Aeza) gets blocked on mobile regardless of how clean the handshake is. That's
  exactly why Aeza is the **EU exit**, not the RU entry.
