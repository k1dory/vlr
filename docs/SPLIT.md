# Split-tunneling (RU-direct domains)

By default every client connection cascades **RU → EU**: the EU exit IP is what
the internet sees. Some domains should instead leave **directly from the RU
node** — your own service domains, and RU sites that need a Russian IP or are
slower/geo-blocked through EU. That is split-tunneling.

## How it works (fwmark, one tunnel)

The RU WireGuard config already routes by fwmark:

```
ip rule add not fwmark 51820 table 51820   # UNmarked traffic -> tunnel -> EU
# WireGuard marks its own packets 51820 -> they fall through to the main table
```

So "go direct from RU" simply means "carry fwmark 51820". The Xray config has two
egress outbounds:

| Outbound | Mark | Path |
|---|---|---|
| `egress` (default) | none | unmarked → tunnel → **EU exit** |
| `egress-ru` | `sockopt.mark = 51820` | bypasses tunnel → **direct RU egress** |

A routing rule sends the RU-direct domain list to `egress-ru`; everything else
falls to `egress` (EU). One tunnel, no extra routing tables. When the cascade is
not enabled, both egress paths are simply direct (the mark is a no-op).

## Managing the list

Seeded at `vlr init` from `.env` `SPLIT_RU_DOMAINS` (comma-separated) plus the
node's own host/`OWN_DOMAIN`. Edit live:

```bash
vlr split list
vlr split add --domain avito.ru          # host + subdomains (domain:avito.ru)
vlr split add --domain full:lk.bank.ru   # exact host only
vlr split add --geosite category-ru      # whole geosite group (needs geosite.dat)
vlr split rm  --domain vk.com
# apply:
vlr render > /usr/local/etc/xray/config.json && systemctl restart xray
```

Matcher forms (passed straight to Xray): a bare host becomes `domain:<host>`
(host + subdomains); `full:host` is exact; `domain:`, `regexp:`, `geosite:` work
as in Xray.

## env.example default

```
SPLIT_RU_DOMAINS=gosuslugi.ru,mos.ru,sberbank.ru,yoomoney.ru,vk.com,mail.ru,yandex.ru,ozon.ru,wildberries.ru,avito.ru
```

Put your own service domains here too (panel, subscription, payment) so
management traffic and "our system" pages always use the RU node and never loop
through EU.

## Verifying

`vlr render` shows the `egress-ru` outbound (with `"mark": 51820`) and the routing
rule. End to end: visit a RU-direct domain and a normal site, check the egress IP
each sees — RU-direct → RU node IP, everything else → EU exit IP.
