#!/usr/bin/env bash
# vlr installer — builds the static binary and installs the global `vlr` command.
# Mirrors the one-button install used by the mtg utility: build -> symlink -> done.
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BIN="$PREFIX/bin/vlr"
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> building vlr (static, no cgo)"
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
  echo "    enable with: systemctl enable --now vlr"
fi

echo
echo "vlr installed: $(command -v vlr)"
vlr version
echo
echo "next:"
echo "  vlr init --role standalone --node-id ru-yc-msk-01 --host <PUBLIC_IP>"
echo "  vlr cascade gen                       # RU->EU WireGuard config"
echo "  vlr user add --email you@example.com  # prints share link"
echo "  systemctl enable --now vlr            # run the node daemon"
