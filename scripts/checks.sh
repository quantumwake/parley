#!/bin/sh
# Runnable checks, one per concern. Invoked by the Makefile:
#
#   make check            offline: vet, race tests, manifests
#   make check-identity   every enrolled identity exchanges a token
#   make check-mcp        the MCP server handshakes, lists tools, and calls one
#   make check-search     labels, find, and a full shared-conversation round trip
#   make check-console    the console API answers with the shape the viewer needs
#   make check-all        all of the above, stopping at the first failure
#
# IDENTITY=<name|path> picks who to act as (default: the first enrolled swarm
# identity, else the machine's configured one).
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
PARLEY="${PARLEY:-parley}"

pass() { printf '  \033[32mok\033[0m   %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1" >&2; exit 1; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# key_file resolves IDENTITY to a key file, or empty for the configured default.
key_file() {
  if [ -n "$IDENTITY" ]; then
    case "$IDENTITY" in
      */*) echo "$IDENTITY" ;;
      *) echo "$HOME/.statefs/identities/$IDENTITY/identity" ;;
    esac
    return
  fi
  first="$(ls "$HOME/.statefs/identities" 2>/dev/null | head -1)"
  [ -n "$first" ] && echo "$HOME/.statefs/identities/$first/identity" || echo ""
}

KF="$(key_file)"
run() { if [ -n "$KF" ]; then STATEFS_KEY_FILE="$KF" "$PARLEY" "$@"; else "$PARLEY" "$@"; fi; }

check_offline() {
  section "offline: vet, tests, manifests"
  GOFLAGS=-mod=vendor go vet ./... || fail "go vet"
  pass "go vet"
  GOFLAGS=-mod=vendor go test -race ./... >/dev/null || fail "go test -race"
  pass "go test -race"
  claude plugin validate . >/dev/null 2>&1 || fail "plugin/marketplace manifest"
  pass "manifests valid"
  v="$(tr -d '[:space:]' < cmd/parley/VERSION)"
  grep -q "\"version\": \"$v\"" .claude-plugin/plugin.json || fail "plugin.json version != $v"
  grep -q "\"version\": \"$v\"" .claude-plugin/marketplace.json || fail "marketplace.json version != $v"
  pass "versions agree ($v)"
}

check_identity() {
  section "identities"
  found=0
  for d in "$HOME"/.statefs/identities/*/; do
    [ -f "$d/identity" ] || continue
    n="$(basename "$d")"
    out="$(STATEFS_KEY_FILE="$d/identity" "$PARLEY" whoami 2>&1)" || fail "$n does not exchange: $out"
    caps="$(echo "$out" | sed -n 's/^caps: //p')"
    echo "$out" | grep -q '^exchange: ok' || fail "$n: no exchange"
    pass "$n  caps=$caps"
    found=$((found + 1))
  done
  [ "$found" -gt 0 ] || fail "no identities enrolled under ~/.statefs/identities"
}

check_mcp() {
  section "mcp server"
  out="$(printf '%s\n%s\n' \
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
    '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | run mcp)" || fail "server exited non-zero"
  echo "$out" | grep -q '"protocolVersion"' || fail "no protocolVersion in initialize"
  pass "initialize"
  for t in search_conversations create_conversation join_conversation post_message read_conversation list_labels whoami; do
    echo "$out" | grep -q "\"$t\"" || fail "tool missing: $t"
  done
  pass "tools/list has the expected tools"
  call="$(printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami","arguments":{}}}' | run mcp)"
  echo "$call" | grep -q '"isError":false' || fail "whoami call reported an error: $call"
  pass "tools/call whoami"
  bad="$(printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"post_message","arguments":{}}}' | run mcp)"
  echo "$bad" | grep -q '"isError":true' || fail "a refused call must report isError"
  pass "a bad call fails in band, not as a protocol error"
}

check_search() {
  section "labels, find, and a conversation round trip"
  run labels --limit 50 | grep -q "conversations visible" || fail "labels"
  pass "labels"
  run find --limit 5 >/dev/null || fail "find"
  pass "find"

  ns="check-$(date +%m%d-%H%M%S)"
  run create "$ns" --description "scripts/checks.sh round trip" --tags check >/dev/null || fail "create $ns"
  pass "create $ns"
  # shellcheck disable=SC2064
  trap "run delete '$ns' >/dev/null 2>&1 || true" EXIT

  run join "$ns" --mode full --as checker >/dev/null || fail "join"
  pass "join --as checker"

  body="$(mktemp)"
  printf '## Heading\n\nBody with a # hash.\n\n- one\n- two\n' > "$body"
  run post "$ns" --kind report --text-file "$body" >/dev/null || fail "post --text-file"
  rm -f "$body"
  pass "post multi-line markdown from a file"

  got="$(run read "$ns" --from 0 --peek)"
  echo "$got" | grep -q '## Heading' || fail "the heading did not survive: $got"
  echo "$got" | grep -q -- '- two' || fail "the list did not survive"
  echo "$got" | grep -q 'checker' || fail "the participant handle is missing"
  pass "read returns the markdown intact, attributed to the handle"

  run read "$ns" --from 99 --peek --wait-seconds 2 | grep -q '0 new rows' || fail "--wait-seconds"
  pass "--wait-seconds returns at the deadline"

  run delete "$ns" >/dev/null || fail "delete"
  trap - EXIT
  pass "delete $ns"
}

check_console() {
  section "console api"
  port=8799
  if [ -n "$KF" ]; then STATEFS_KEY_FILE="$KF" "$PARLEY" console --listen "127.0.0.1:$port" --no-open >/dev/null 2>&1 &
  else "$PARLEY" console --listen "127.0.0.1:$port" --no-open >/dev/null 2>&1 & fi
  pid=$!
  # shellcheck disable=SC2064
  trap "kill $pid 2>/dev/null || true" EXIT
  i=0
  while [ $i -lt 30 ]; do
    curl -fsS "http://127.0.0.1:$port/v1/me" >/dev/null 2>&1 && break
    i=$((i + 1)); sleep 1
  done
  [ $i -lt 30 ] || fail "console did not answer on $port"
  pass "/v1/me"
  curl -fsS "http://127.0.0.1:$port/v1/conversations" | python3 -c "
import json,sys
rows=json.load(sys.stdin)['conversations']
assert rows, 'no conversations returned'
sess=[r for r in rows if r.get('mode') != 'shared']
dated=[r for r in sess if r.get('started_ms')]
active=[r for r in sess if r.get('active_ms')]
assert all(isinstance(r['active_ms'], int) for r in active), 'active_ms must be epoch ms'
print(f'    {len(rows)} conversations, {len(sess)} sessions, {len(dated)} dated, {len(active)} with last activity')
" || fail "/v1/conversations shape"
  pass "/v1/conversations groups by date and last activity"
  kill $pid 2>/dev/null || true
  trap - EXIT
}

case "${1:-all}" in
  offline)  check_offline ;;
  identity) check_identity ;;
  mcp)      check_mcp ;;
  search)   check_search ;;
  console)  check_console ;;
  all)      check_offline; check_identity; check_mcp; check_search; check_console ;;
  *) echo "usage: $0 offline|identity|mcp|search|console|all" >&2; exit 2 ;;
esac

printf '\n\033[32mall checks passed\033[0m\n'
