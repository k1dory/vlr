# Uninstall — declarative rollback

Install and uninstall are symmetric. Everything the install touches is recorded
in an append-only **ledger** (`/etc/vlr/ledger.jsonl`), and `vlr uninstall`
reverses it in safe order, idempotently. It also works **without** the ledger:
the canonical resource set (binary, unit, `/etc/vlr`, the cascade interface, the
EU host) is derived from the node config as a fallback.

```bash
vlr uninstall                 # asks for confirmation, shows the plan first
vlr uninstall --yes           # no prompt
vlr uninstall --keep-config   # keep /etc/vlr (config, state, ledger)
vlr uninstall --skip-eu       # don't touch the remote EU exit
vlr uninstall --remove-go     # also remove Go IF vlr installed it
vlr uninstall --remove-packages  # also apt-remove packages vlr installed
vlr uninstall --eu-key ~/.ssh/id_ed25519   # creds for EU teardown (or --eu-pass)
```

## What it reverses, in order

1. **Stop the daemon** — `systemctl disable --now vlr`.
2. **EU exit (remote)** — SSH in and `wg-quick down` + disable + remove the conf
   and keys. Needs SSH creds again (see below).
3. **Local WireGuard** — `wg-quick down <iface>`, disable `wg-quick@<iface>`,
   remove the lingering split-tunnel `ip rule`, delete `/etc/wireguard/<iface>.conf`.
4. **systemd unit** — remove the unit, `daemon-reload`, `reset-failed`.
5. **Go** — only if vlr installed it *and* `--remove-go`.
6. **Packages** — only ones vlr installed *and* `--remove-packages`.
7. **Binary** — `/usr/local/bin/vlr` and any other recorded files.
8. **`/etc/vlr`** — last (it holds the ledger we are reading), unless `--keep-config`.

## The ledger

`vlr ledger list` shows what is tracked. install.sh records the binary, the
systemd unit/enable, and the Go toolchain (only if it installed Go). `vlr init`
records the config dir/file; `vlr cascade up` records the local WG interface and
the EU exit (host/user/port/key — **never the password**).

## Designed-for failure modes

- **Don't break pre-existing software.** Go and the `wireguard` package are
  removed only if vlr installed them (a ledger flag) *and* you opt in. By default
  they stay.
- **EU teardown needs creds again.** The password is never stored. Pass
  `--eu-key`/`--eu-pass`, or `--skip-eu` to leave the EU box alone (and tear it
  down manually later: `wg-quick down wg-cascade`).
- **Self-deletion.** Removing the running `/usr/local/bin/vlr` is fine on Linux
  (unlink-after-exec); it is deleted near the end.
- **Ledger lives in `/etc/vlr`**, which is removed last — it is loaded into memory
  first.
- **Rebooted / already clean.** Every step tolerates "already gone"; uninstall is
  safe to re-run.
- **User-edited files are left alone** (`.env`, a hand-written Xray config) — they
  are reported, not deleted.
- **Guardrails.** Recursive delete refuses `/`, `/etc`, `/usr`.
