#!/bin/sh
# Release the plugin and prove it end to end, without needing anyone to do it
# for you.
#
#   scripts/release-and-test.sh "commit message"
#
# What it does, stopping at the first failure:
#   1. checks the three version files agree,
#   2. builds the viewer and the binary, and runs the tests (a release must
#      never ship a failing test),
#   3. validates the plugin and marketplace manifests,
#   4. commits and pushes, because the marketplace is a clone of the GitHub
#      remote: nothing is installable until it is pushed,
#   5. updates the installed plugin and reports what landed,
#   6. runs a swarm against the new build and prints the channel.
#
# Options:
#   --no-test     skip step 6
#   --no-push     stop after step 3 (build and verify only; nothing leaves
#                 this machine)
#   --agents a,b  who to run the swarm as (default: the enrolled swarm ones)
#   --example E   naming | parallel-docs | code-review | estimation | debate
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

MSG=""
RUN_TEST=1
PUSH=1
AGENTS=""
EXAMPLE="parallel-docs"
MINUTES="4"
while [ $# -gt 0 ]; do
  case "$1" in
    --no-test) RUN_TEST=0 ;;
    --no-push) PUSH=0 ;;
    --agents) AGENTS="$2"; shift ;;
    --example) EXAMPLE="$2"; shift ;;
    --minutes) MINUTES="$2"; shift ;;
    -h|--help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) MSG="$1" ;;
  esac
  shift
done

say() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
die() { printf '\n\033[31m!! %s\033[0m\n' "$1" >&2; exit 1; }

# ---------------------------------------------------------------- 1. versions
say "versions"
VER="$(tr -d '[:space:]' < cmd/parley/VERSION)"
PLUGIN_VER="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' .claude-plugin/plugin.json | head -1)"
MARKET_VER="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' .claude-plugin/marketplace.json | head -1)"
printf '  cmd/parley/VERSION           %s\n  plugin.json                 %s\n  marketplace.json            %s\n' \
  "$VER" "$PLUGIN_VER" "$MARKET_VER"
[ "$VER" = "$PLUGIN_VER" ] || die "plugin.json says $PLUGIN_VER, VERSION says $VER"
[ "$VER" = "$MARKET_VER" ] || die "marketplace.json says $MARKET_VER, VERSION says $VER"

# ------------------------------------------------------------- 2. build, test
if command -v npm >/dev/null 2>&1; then
  say "viewer"
  (cd console && npm install --silent >/dev/null 2>&1 && npm run build >/dev/null) || die "viewer build failed"
  echo "  built into cmd/parley/dist"
fi

say "tests"
GOFLAGS=-mod=vendor go vet ./... || die "vet failed"
GOFLAGS=-mod=vendor go test -race ./... || die "tests failed - not releasing"

say "binary"
GOFLAGS=-mod=vendor go build -o "${CLAUDE_PLUGIN_DATA:-$HOME/.statefs-ai}/bin/parley" ./cmd/parley
echo "  $(parley version 2>/dev/null || echo 'not on PATH yet')"

# -------------------------------------------------------------- 3. manifests
say "manifests"
claude plugin validate . || die "manifest validation failed"
grep -q '"mcpServers"' .claude-plugin/plugin.json && echo "  plugin.json declares an MCP server" || die "plugin.json declares no MCP server"
[ ! -f .mcp.json ] || die ".mcp.json at the repo root is read as a project MCP config with CLAUDE_PLUGIN_ROOT unset (ENOENT); declare the server in plugin.json"

if [ "$PUSH" = 0 ]; then
  say "stopping before push (--no-push)"
  exit 0
fi

# ------------------------------------------------------------ 4. commit, push
say "commit and push"
if [ -n "$(git status --porcelain)" ]; then
  [ -n "$MSG" ] || die 'there are changes but no commit message: scripts/release-and-test.sh "what changed"'
  git add -A
  git commit -q -m "$MSG"
  echo "  committed: $MSG"
else
  echo "  nothing to commit"
fi

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
git push -q origin "$BRANCH" || die "push failed"
echo "  pushed $BRANCH ($(git rev-parse --short HEAD))"

# --------------------------------------------------------------- 5. reinstall
say "update the installed plugin"
claude plugin update parley@statefs-ai || die "plugin update failed"
INSTALLED="$(sed -n 's/.*"installPath": *"\([^"]*\/parley\/[^"]*\)".*/\1/p' "$HOME/.claude/plugins/installed_plugins.json" | tail -1)"
echo "  installed at: $INSTALLED"
grep -q '"mcpServers"' "$INSTALLED/.claude-plugin/plugin.json" && echo "  MCP server declared in the installed copy" || echo "  NOTE: no MCP server in the installed plugin.json"
echo "  NOTE: a running session keeps the plugin root it started with. Start a new session to pick this up."

# -------------------------------------------------------------------- 6. test
if [ "$RUN_TEST" = 0 ]; then
  say "done (skipping the swarm, --no-test)"
  exit 0
fi

if [ -z "$AGENTS" ]; then
  AGENTS="$(ls "$HOME/.statefs/identities" 2>/dev/null | tr '\n' ',' | sed 's/,$//')"
fi

[ -n "$AGENTS" ] || { say "no enrolled identities under ~/.statefs/identities - skipping the swarm"; exit 0; }

say "swarm: $AGENTS"
python3 -u scripts/swarm.py run --agents "$AGENTS" --minutes "$MINUTES" --interval 20 \
  --model haiku --example "$EXAMPLE" --drive self
