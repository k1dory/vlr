# Split-tunneling (RU-direct domains)

By default every client connection cascades **RU → EU**: the EU exit IP is what
the internet sees. Some domains should instead leave **directly from the RU
node** — your own service domains, and RU sites that need a Russian IP or are
slower/geo-blocked through EU. That is split-tunneling.

## How it works (fwmark, one tunnel)

The RU WireGuard config routes **only marked** traffic into the tunnel:

```
ip rule add fwmark 51820 table 51820   # ONLY fwmark-51820 traffic -> tunnel -> EU
```

So "go to EU" means "carry fwmark 51820"; everything unmarked stays direct. The
Xray config has two egress outbounds:

| Outbound | Mark | Path |
|---|---|---|
| `egress` (default) | `sockopt.mark = 51820` | marked → tunnel → **EU exit** |
| `egress-ru` | none | unmarked → main table → **direct RU egress** |

A routing rule sends the RU-direct domain list to `egress-ru` (direct); everything
else falls to `egress` (EU). Crucially, the **node's own traffic is also unmarked**,
so it egresses directly — a remote `wg-quick up` never captures your SSH session.
One tunnel, no extra tables. When the cascade is off, marked traffic just has no
tunnel to enter and goes direct too (no-op).

> Xray must run with `CAP_NET_ADMIN` (it runs as **root** by default, which has it)
> to set `SO_MARK`. Without it, EU-bound traffic can't be marked and would leak
> out directly.

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
