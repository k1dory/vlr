#!/usr/bin/env bash
# vlr installer — builds the static binary and installs the global `vlr` command.
# Mirrors the one-button install used by the mtg utility: build -> symlink -> done.
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BIN="$PREFIX/bin/vlr"
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_VER="${GO_VER:-1.25.1}"

# Ensure a recent Go toolchain. Fresh RU/EU nodes ship without Go, and the distro
# package is usually too old for go.mod's `go 1.25`. Install the official binary
# (set VLR_AUTO_GO=0 to disable and just print instructions instead).
ensure_go() {
  if command -v go >/dev/null 2>&1; then
    return 0
  fi
  case "$(uname -m)" in
    x86_64|amd64)  goarch=amd64;;
    aarch64|arm64) goarch=arm64;;
    *) echo "unknown arch $(uname -m); install Go ${GO_VER} manually"; exit 1;;
  esac
  if [ "${VLR_AUTO_GO:-1}" != "1" ]; then
    echo "go not found. Install it:"
    echo "  curl -fsSL https://go.dev/dl/go${GO_VER}.linux-${goarch}.tar.gz -o /tmp/go.tgz"
    echo "  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz"
    echo "  export PATH=\$PATH:/usr/local/go/bin"
    exit 1
  fi
  echo "==> go not found; installing Go ${GO_VER} (${goarch})"
  curl -fsSL "https://go.dev/dl/go${GO_VER}.linux-${goarch}.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
  echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh >/dev/null
  export PATH="$PATH:/usr/local/go/bin"
  command -v go >/dev/null 2>&1 || { echo "Go install failed"; exit 1; }
}

ensure_go
echo "==> building vlr with $(go version)"
cd "$SRC_DIR"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o vlr ./cmd/vlr

echo "==> installing to $BIN"
if [ -w "$PREFIX/bin" ]; then
  install -m 0755 vlr "$BIN"
else
  sudo install -m 0755 vlr "$BIN"
fi

echo "==> installing systemd unit (if systemd present)"
if command -v systemctl >/dev/null 2>&1; then
  sudo cp deploy/systemd/vlr.service /etc/systemd/system/vlr.service
  sudo systemctl daemon-reload
fi

echo
echo "vlr установлен: $(command -v vlr)  ($(vlr version))"

CONFIG="/etc/vlr/config.json"

# Interactive provisioning: show the mode menu, create the config, enable the
# service. Only on a real terminal — in a pipe/CI we just print the next step.
if [ -t 0 ] && [ -t 1 ]; then
  # seed .env from the example so DOMAIN_FOR_TLS (console.yandex.cloud) applies
  [ -f "$SRC_DIR/.env" ] || cp "$SRC_DIR/env.example" "$SRC_DIR/.env" 2>/dev/null || true
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
    sleep 1
    sudo systemctl --no-pager --full status vlr | head -n 6 || true
  fi
  echo
  echo "Готово. Дальше — каскад RU→EU (одной командой с этой ноды) и Xray:"
  echo "  vlr cascade up --eu-host <EU_IP> --eu-user root --eu-key ~/.ssh/id_ed25519"
  echo "  vlr render > /usr/local/etc/xray/config.json && systemctl restart xray"
else
  echo "next: sudo vlr init    # интерактивное меню режимов (1/2/3), затем: systemctl enable --now vlr"
fi
