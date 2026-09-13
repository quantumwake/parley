#!/bin/sh
# statefs.ai parley installer.
#   curl -fsSL https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh
# Install and enroll this machine in one step, with the URL from app.statefs.ai:
#   curl -fsSL https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh -s -- '<enrollment url>'
# (or PARLEY_ENROLL_URL='<enrollment url>'; the passphrase, if the URL has one, in STATEFS_ENROLL_PASSPHRASE)
# While the repo is private, fetch with a token:
#   curl -fsSL -H "Authorization: token $(gh auth token)" https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh
# Options (env): PARLEY_VERSION=v0.2.6  PARLEY_DIR=~/.local/bin  GITHUB_TOKEN=... (private repo)
set -e
REPO="quantumwake/parley"
ENROLL_URL="${1:-${PARLEY_ENROLL_URL:-}}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"; ARCH="$(uname -m)"
case "$ARCH" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "unsupported arch $ARCH" >&2; exit 1 ;; esac
case "$OS" in darwin|linux) ;; *) echo "use install.ps1 on Windows" >&2; exit 1 ;; esac
TOKEN="${GITHUB_TOKEN:-}"
if [ -z "$TOKEN" ] && command -v gh >/dev/null 2>&1; then TOKEN="$(gh auth token 2>/dev/null || true)"; fi
auth() { if [ -n "$TOKEN" ]; then printf 'Authorization: token %s' "$TOKEN"; else printf 'X-None: 1'; fi; }
if [ -z "${PARLEY_VERSION:-}" ]; then
  PARLEY_VERSION="$(curl -fsSL -H "$(auth)" "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
fi
[ -n "$PARLEY_VERSION" ] || { echo "could not determine the latest release (private repo? set GITHUB_TOKEN or log in with gh)" >&2; exit 1; }
ASSET="parley_${OS}_${ARCH}"
DIR="${PARLEY_DIR:-$HOME/.local/bin}"; mkdir -p "$DIR" "$HOME/.statefs-ai/bin"
TMP="$(mktemp)"
# release assets on a private repo need the API asset URL + octet-stream accept
ASSET_URL="$(curl -fsSL -H "$(auth)" "https://api.github.com/repos/$REPO/releases/tags/$PARLEY_VERSION" | tr ',' '\n' | grep -A0 "\"name\": *\"$ASSET\"" -B6 | sed -n 's/.*"url": *"\(https:\/\/api.github.com\/repos\/[^"]*\/assets\/[0-9]*\)".*/\1/p' | head -1)"
[ -n "$ASSET_URL" ] || { echo "no asset $ASSET in $PARLEY_VERSION" >&2; exit 1; }
curl -fsSL -H "$(auth)" -H "Accept: application/octet-stream" "$ASSET_URL" -o "$TMP"
chmod +x "$TMP"; mv "$TMP" "$HOME/.statefs-ai/bin/parley"; ln -sf "$HOME/.statefs-ai/bin/parley" "$DIR/parley"
echo "installed parley $PARLEY_VERSION -> $DIR/parley"
case ":$PATH:" in *":$DIR:"*) ;; *) echo "note: $DIR is not on your PATH; add:  export PATH=\"$DIR:\$PATH\"" ;; esac
if command -v claude >/dev/null 2>&1; then
  claude plugin marketplace add "$REPO" >/dev/null 2>&1 || true
  claude plugin install parley@parley --scope user >/dev/null 2>&1 && echo "Claude Code plugin parley@parley installed (restart Claude Code sessions to load it)" || echo "note: install the plugin inside Claude Code:  /plugin marketplace add $REPO  then  /plugin install parley@parley"
else
  echo "note: Claude Code not found on PATH; inside Claude Code run  /plugin marketplace add $REPO  then  /plugin install parley@parley"
fi
if [ -n "$ENROLL_URL" ]; then
  echo "enrolling this machine..."
  "$DIR/parley" enroll "$ENROLL_URL"
  echo "done: new Claude Code sessions on this machine are recorded (restart any that are open)"
else
  echo "next: sign in at https://app.statefs.ai, add this machine, and run the command it shows (or: parley enroll '<enrollment url>')"
fi
