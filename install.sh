#!/usr/bin/env bash
# vlr installer — installs the global `vlr` command + systemd unit.
#
# Default: download a prebuilt static binary from GitHub Releases (no Go needed):
#   curl -fsSL https://github.com/k1dory/vlr/releases/latest/download/install.sh | sudo bash
#   ./install.sh                         # same, from a clone
#   VLR_VERSION=v0.2.0 ./install.sh      # pin a release tag (default: latest)
#
# From source (old behaviour: installs Go if missing and builds the clone):
#   ./install.sh --from-source           # or VLR_FROM_SOURCE=1 ./install.sh
#
# Env: VLR_REPO (default k1dory/vlr), VLR_VERSION, PREFIX (default /usr/local),
#      VLR_DOWNLOAD_BASE (mirror holding the release assets; needs VLR_VERSION),
#      GO_VER / VLR_AUTO_GO=0 (source mode only).
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BIN="$PREFIX/bin/vlr"
CONFIG="/etc/vlr/config.json"
ENV_FILE="/etc/vlr/.env"
UNIT="/etc/systemd/system/vlr.service"
REPO="${VLR_REPO:-k1dory/vlr}"
VERSION="${VLR_VERSION:-latest}"
FROM_SOURCE="${VLR_FROM_SOURCE:-0}"

for arg in "$@"; do
  case "$arg" in
    --from-source) FROM_SOURCE=1;;
    --version=*)   VERSION="${arg#--version=}";;
    -h|--help)     sed -n '2,15p' "$0" 2>/dev/null || true; exit 0;;
    *) echo "unknown argument: $arg" >&2; exit 2;;
  esac
done

# SRC_DIR is the clone this script lives in; empty when piped via curl | bash.
SRC_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
  SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi

SUDO=""
[ "$(id -u)" -eq 0 ] || SUDO="sudo"

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo amd64;;
    aarch64|arm64) echo arm64;;
    *) echo "unsupported arch $(uname -m)" >&2; exit 1;;
  esac
}

# rec records an install action into the ledger via the freshly-installed binary.
# Best-effort: never fail the install if recording fails.
rec() { $SUDO "$BIN" ledger record --config "$CONFIG" --kind "$1" --target "$2" 2>/dev/null || true; }

# ---------------------------------------------------------------- release mode
# Downloads vlr_<tag>_linux_<arch>.tar.gz + SHA256SUMS, verifies the checksum
# and leaves the unpacked bundle (vlr, vlr.service, env.example) in $STAGE.
fetch_release() {
  local arch tag base tarball
  arch="$(detect_arch)"
  command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
  if [ -n "${VLR_DOWNLOAD_BASE:-}" ]; then
    [ "$VERSION" != "latest" ] || { echo "VLR_DOWNLOAD_BASE требует VLR_VERSION=<tag>" >&2; exit 1; }
    tag="$VERSION"
  elif [ "$VERSION" = "latest" ]; then
    # /releases/latest redirects to /releases/tag/<tag>; take the tag from the URL.
    tag="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')"
    case "$tag" in v*) ;; *) echo "не удалось определить последний релиз $REPO (нет релизов?)" >&2; exit 1;; esac
  else
    tag="$VERSION"
  fi
  base="${VLR_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download/$tag}"
  tarball="vlr_${tag}_linux_${arch}.tar.gz"
  echo "==> скачиваю vlr $tag (linux/$arch)"
  curl -fsSL "$base/$tarball" -o "$STAGE/$tarball"
  curl -fsSL "$base/SHA256SUMS" -o "$STAGE/SHA256SUMS"
  (cd "$STAGE" && grep " $tarball\$" SHA256SUMS | sha256sum -c --quiet -) \
    || { echo "checksum mismatch for $tarball" >&2; exit 1; }
  tar -xzf "$STAGE/$tarball" -C "$STAGE"
}

# ----------------------------------------------------------------- source mode
# have_go_125 succeeds if a Go >= 1.25 (go.mod's minimum) is on PATH.
have_go_125() {
  command -v go >/dev/null 2>&1 || return 1
  local ver major minor
  ver="$(go version | awk '{print $3}' | sed 's/^go//')"
  major="${ver%%.*}"; minor="${ver#*.}"; minor="${minor%%.*}"
  [ "${major:-0}" -gt 1 ] 2>/dev/null && return 0
  [ "${major:-0}" -eq 1 ] 2>/dev/null && [ "${minor:-0}" -ge 25 ] 2>/dev/null && return 0
  return 1
}

# Ensure a recent Go toolchain (installs the latest official release; override
# with GO_VER=1.25.4; VLR_AUTO_GO=0 only prints instructions).
ensure_go() {
  export PATH="$PATH:/usr/local/go/bin"
  if have_go_125; then echo "==> Go уже есть: $(go version)"; return 0; fi
  local goarch ver
  goarch="$(detect_arch)"
  ver="${GO_VER:-}"
  if [ -z "$ver" ]; then
    ver="$(curl -fsSL 'https://go.dev/VERSION?m=text' 2>/dev/null | head -1 | sed 's/^go//')"
    [ -n "$ver" ] || ver="1.25.4"
  fi
  if [ "${VLR_AUTO_GO:-1}" != "1" ]; then
    echo "go not found. Install it:"
    echo "  curl -fsSL https://go.dev/dl/go${ver}.linux-${goarch}.tar.gz -o /tmp/go.tgz"
    echo "  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz"
    exit 1
  fi
  echo "==> Go не найден (или старый); ставлю Go ${ver} (${goarch})"
  curl -fsSL "https://go.dev/dl/go${ver}.linux-${goarch}.tar.gz" -o "$STAGE/go.tgz"
  $SUDO rm -rf /usr/local/go && $SUDO tar -C /usr/local -xzf "$STAGE/go.tgz"
  # shellcheck disable=SC2016  # literal $PATH goes into the profile script
  echo 'export PATH=$PATH:/usr/local/go/bin' | $SUDO tee /etc/profile.d/go.sh >/dev/null
  have_go_125 || { echo "Go install failed"; exit 1; }
  GO_INSTALLED_BY_VLR=1   # so `vlr uninstall --remove-go` knows it may remove it
}

build_from_source() {
  [ -n "$SRC_DIR" ] && [ -f "$SRC_DIR/go.mod" ] \
    || { echo "--from-source нужно запускать из клона репозитория" >&2; exit 1; }
  ensure_go
  local ver
  ver="$(cd "$SRC_DIR" && git describe --tags --always --dirty 2>/dev/null || echo dev)"
  echo "==> собираю vlr $ver из $SRC_DIR ($(go version))"
  (cd "$SRC_DIR" && CGO_ENABLED=0 go build -trimpath \
     -ldflags="-s -w -X main.version=$ver" -o "$STAGE/vlr" ./cmd/vlr)
  cp "$SRC_DIR/deploy/systemd/vlr.service" "$STAGE/vlr.service"
  cp "$SRC_DIR/env.example" "$STAGE/env.example"
}

# ------------------------------------------------------------------- install
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if [ "$FROM_SOURCE" = "1" ]; then build_from_source; else fetch_release; fi

echo "==> устанавливаю в $BIN"
$SUDO install -d -m 0755 "$PREFIX/bin" /etc/vlr
$SUDO install -m 0755 "$STAGE/vlr" "$BIN"
rec file "$BIN"
[ "${GO_INSTALLED_BY_VLR:-0}" = "1" ] && rec go-toolchain /usr/local/go

# Seed /etc/vlr/.env (DOMAIN_FOR_TLS / OWN_DOMAIN / SPLIT_RU_DOMAINS) for the very
# first `vlr init`. A .env in the clone (cwd) still wins — see util.dotenv.
if [ ! -f "$ENV_FILE" ]; then
  if [ -n "$SRC_DIR" ] && [ -f "$SRC_DIR/.env" ]; then
    $SUDO install -m 0600 "$SRC_DIR/.env" "$ENV_FILE"
  else
    $SUDO install -m 0600 "$STAGE/env.example" "$ENV_FILE"
  fi
  echo "==> создал $ENV_FILE (домены-приманки и split — правь там до vlr init)"
fi

if command -v systemctl >/dev/null 2>&1; then
  echo "==> ставлю systemd-юнит"
  $SUDO install -m 0644 "$STAGE/vlr.service" "$UNIT"
  $SUDO systemctl daemon-reload
  rec systemd-unit "$UNIT"
  # Upgrade path: a running daemon picks up the new binary.
  if systemctl is-active --quiet vlr; then
    $SUDO systemctl restart vlr && echo "==> vlr перезапущен на новой версии"
  fi
fi

echo
echo "vlr установлен: $BIN  ($("$BIN" version))"

# Interactive provisioning on a real terminal. Under `curl | bash` stdin is the
# pipe, so the wizard reads from /dev/tty when one is attached.
TTY_IN=""
if [ -t 0 ]; then TTY_IN=/dev/stdin
elif [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; then TTY_IN=/dev/tty
fi
if [ -n "$TTY_IN" ] && [ -t 1 ]; then
  if [ -f "$CONFIG" ]; then
    echo "конфиг уже есть: $CONFIG  (перенастроить: vlr init)"
  else
    echo "  (SNI берётся из $ENV_FILE → DOMAIN_FOR_TLS; свой домен — OWN_DOMAIN)"
    $SUDO "$BIN" init --config "$CONFIG" <"$TTY_IN" || {
      echo "настройка прервана — запусти позже: sudo vlr init"
      exit 0
    }
  fi
  if [ -f "$CONFIG" ] && command -v systemctl >/dev/null 2>&1; then
    echo "==> включаю и запускаю сервис vlr"
    $SUDO systemctl enable --now vlr
    rec systemd-enabled vlr
    sleep 1
    $SUDO systemctl --no-pager --full status vlr | head -n 6 || true
  fi
  echo
  echo "Дальше — каскад RU→EU (одной командой с этой ноды) и Xray:"
  echo "  vlr cascade up --eu-host <EU_IP> --eu-user root --eu-key ~/.ssh/id_ed25519"
  echo "  vlr up         # поставит/обновит Xray, применит конфиг, перезапустит + диагностика"
else
  echo "next: sudo vlr init    # интерактивное меню режимов (1/2/3), затем: systemctl enable --now vlr"
fi
