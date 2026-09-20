#!/bin/sh
# statefs.ai parley installer.
#   curl -fsSL https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh
# Install and enroll this machine in one step, with the URL from app.statefs.ai:
#   curl -fsSL https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh -s -- '<enrollment url>'
# (or PARLEY_ENROLL_URL='<enrollment url>'; the passphrase, if the URL has one, in STATEFS_ENROLL_PASSPHRASE)
# Options (env): PARLEY_VERSION=v0.3.21  PARLEY_DIR=~/.local/bin  GITHUB_TOKEN=... (optional, API rate limits)
set -e
REPO="quantumwake/parley"
ENROLL_URL="${1:-${PARLEY_ENROLL_URL:-}}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"; ARCH="$(uname -m)"
case "$ARCH" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "unsupported arch $ARCH" >&2; exit 1 ;; esac
case "$OS" in darwin|linux) ;; *) echo "use install.ps1 on Windows" >&2; exit 1 ;; esac
ASSET="parley_${OS}_${ARCH}"
DIR="${PARLEY_DIR:-$HOME/.local/bin}"; mkdir -p "$DIR" "$HOME/.statefs-ai/bin"
TMP="$(mktemp)"
if [ -n "${PARLEY_VERSION:-}" ]; then
  curl -fsSL "https://github.com/$REPO/releases/download/$PARLEY_VERSION/$ASSET" -o "$TMP"
else
  curl -fsSL "https://github.com/$REPO/releases/latest/download/$ASSET" -o "$TMP"
  PARLEY_VERSION="$(basename "$(curl -fsSI "https://github.com/$REPO/releases/latest" | tr -d '\r' | awk -F/ '/^[Ll]ocation:/{print $NF; exit}')")"
  [ -n "$PARLEY_VERSION" ] || PARLEY_VERSION=latest
fi
chmod +x "$TMP"; mv "$TMP" "$HOME/.statefs-ai/bin/parley"; ln -sf "$HOME/.statefs-ai/bin/parley" "$DIR/parley"
echo "installed parley $PARLEY_VERSION -> $DIR/parley"
case ":$PATH:" in *":$DIR:"*) ;; *) echo "note: $DIR is not on your PATH; add:  export PATH=\"$DIR:\$PATH\"" ;; esac
"$DIR/parley" setup auto
if [ -n "$ENROLL_URL" ]; then
  echo "enrolling this machine..."
  "$DIR/parley" enroll "$ENROLL_URL"
  echo "done: new agent sessions on this machine are recorded (restart any that are open)"
else
  echo "next: sign in at https://app.statefs.ai, add this machine, and run the command it shows (or: parley enroll '<enrollment url>')"
fi
