#!/usr/bin/env bash
# vlr installer — builds the static binary and installs the global `vlr` command.
# Mirrors the one-button install used by the mtg utility: build -> symlink -> done.
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BIN="$PREFIX/bin/vlr"
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Seed .env from the example on first run (unconditionally), so DOMAIN_FOR_TLS /
# OWN_DOMAIN / SPLIT_RU_DOMAINS apply on the very first `vlr init`.
if [ ! -f "$SRC_DIR/.env" ] && [ -f "$SRC_DIR/env.example" ]; then
  cp "$SRC_DIR/env.example" "$SRC_DIR/.env"
  echo "==> создал .env из env.example (правь домены там)"
fi

# A Go installed to /usr/local/go is only on PATH via /etc/profile.d/go.sh, which
# applies to future login shells — not to this script or a re-run in the same
# session. Add it here so an already-installed Go is actually found (the reason
# install.sh kept reinstalling Go every run).
export PATH="$PATH:/usr/local/go/bin"

# have_go_125 succeeds if a Go >= 1.25 (go.mod's minimum) is on PATH.
have_go_125() {
  command -v go >/dev/null 2>&1 || return 1
  local ver major minor
  ver="$(go version | awk '{print $3}' | sed 's/^go//')"   # e.g. 1.25.4
  major="${ver%%.*}"; minor="${ver#*.}"; minor="${minor%%.*}"
  [ "${major:-0}" -gt 1 ] 2>/dev/null && return 0
  [ "${major:-0}" -eq 1 ] 2>/dev/null && [ "${minor:-0}" -ge 25 ] 2>/dev/null && return 0
  return 1
}

# Ensure a recent Go toolchain. Fresh RU/EU nodes ship without Go (or too old for
# go.mod's `go 1.25`). Installs the LATEST official release (override with
# GO_VER=1.25.4; VLR_AUTO_GO=0 disables and just prints instructions).
ensure_go() {
  if have_go_125; then
    echo "==> Go уже есть: $(go version)"
    return 0
  fi
  case "$(uname -m)" in
    x86_64|amd64)  goarch=amd64;;
    aarch64|arm64) goarch=arm64;;
    *) echo "unknown arch $(uname -m); install Go manually"; exit 1;;
  esac
  # Latest version from go.dev (e.g. "go1.25.4" -> "1.25.4"); fall back if offline.
  local ver="${GO_VER:-}"
  if [ -z "$ver" ]; then
    ver="$(curl -fsSL https://go.dev/VERSION?m=text 2>/dev/null | head -1 | sed 's/^go//')"
    [ -n "$ver" ] || ver="1.25.4"
  fi
  if [ "${VLR_AUTO_GO:-1}" != "1" ]; then
    echo "go not found. Install it:"
    echo "  curl -fsSL https://go.dev/dl/go${ver}.linux-${goarch}.tar.gz -o /tmp/go.tgz"
    echo "  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz"
    echo "  export PATH=\$PATH:/usr/local/go/bin"
    exit 1
  fi
  echo "==> Go не найден (или старый); ставлю Go ${ver} (${goarch})"
  curl -fsSL "https://go.dev/dl/go${ver}.linux-${goarch}.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
  echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh >/dev/null
  export PATH="$PATH:/usr/local/go/bin"
  have_go_125 || { echo "Go install failed"; exit 1; }
  GO_INSTALLED_BY_VLR=1   # so `vlr uninstall --remove-go` knows it may remove it
}

# rec records an install action into the ledger via the freshly-installed binary.
# Best-effort: never fail the install if recording fails.
rec() { sudo "$BIN" ledger record --config "$CONFIG" --kind "$1" --target "$2" 2>/dev/null || true; }

ensure_go
echo "==> building vlr with $(go version)"
cd "$SRC_DIR"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o vlr ./cmd/vlr

CONFIG="/etc/vlr/config.json"

echo "==> installing to $BIN"
if [ -w "$PREFIX/bin" ]; then
  install -m 0755 vlr "$BIN"
else
  sudo install -m 0755 vlr "$BIN"
fi
# From here the binary exists, so it can own the install ledger.
rec file "$BIN"
[ "${GO_INSTALLED_BY_VLR:-0}" = "1" ] && rec go-toolchain /usr/local/go

echo "==> installing systemd unit (if systemd present)"
if command -v systemctl >/dev/null 2>&1; then
  sudo cp deploy/systemd/vlr.service /etc/systemd/system/vlr.service
  sudo systemctl daemon-reload
  rec systemd-unit /etc/systemd/system/vlr.service
fi

echo
echo "vlr установлен: $(command -v vlr)  ($(vlr version))"

# Interactive provisioning: show the mode menu, create the config, enable the
# service. Only on a real terminal — in a pipe/CI we just print the next step.
if [ -t 0 ] && [ -t 1 ]; then
  if [ -f "$CONFIG" ]; then
    echo "конфиг уже есть: $CONFIG  (перенастроить: vlr init)"
  else
    echo "  (SNI берётся из $SRC_DIR/.env → DOMAIN_FOR_TLS; свой домен — OWN_DOMAIN)"
    sudo "$BIN" init --config "$CONFIG" || {
      echo "настройка прервана — запусти позже: sudo vlr init"
      exit 0
    }
  fi
  if [ -f "$CONFIG" ] && command -v systemctl >/dev/null 2>&1; then
    echo "==> включаю и запускаю сервис vlr"
    sudo systemctl enable --now vlr
    rec systemd-enabled vlr
    sleep 1
    sudo systemctl --no-pager --full status vlr | head -n 6 || true
  fi
  echo
  echo "Готово. Дальше — каскад RU→EU (одной командой с этой ноды) и Xray:"
  echo "  vlr cascade up --eu-host <EU_IP> --eu-user root --eu-key ~/.ssh/id_ed25519"
  echo "  vlr up         # поставит/обновит Xray, применит конфиг, перезапустит + диагностика"
else
  echo "next: sudo vlr init    # интерактивное меню режимов (1/2/3), затем: systemctl enable --now vlr"
fi
