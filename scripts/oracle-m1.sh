#!/usr/bin/env bash
# M1 oracle: capture one real headless Claude Code session through the
# plugin and prove the replay matches the transcript (0 missing, 0
# duplicated) with session.end last.
#
# Offline (file store):   scripts/oracle-m1.sh
# Against statefs.io:     STATEFS_DIRECTORY=https://directory.statefs.io scripts/oracle-m1.sh
#   (needs an enrolled identity: STATEFS_KEY_FILE or ~/.statefs/identity)
set -euo pipefail
cd "$(dirname "$0")/.."
make plugin >/dev/null
DATA=~/.claude/plugins/data/statefs-ai-inline
WORK=${WORK:-$(mktemp -d)}
export STATEFS_KEY_FILE=${STATEFS_KEY_FILE:-$WORK/identity}
if [ -z "${STATEFS_DIRECTORY:-}" ]; then
  export STATEFS_AI_STORE="file:$WORK/store"
  # offline identity so the author is known (no directory involved)
  [ -f "$STATEFS_KEY_FILE" ] || python3 - "$STATEFS_KEY_FILE" <<'PY'
import json,sys,base64,os
from pathlib import Path
# a throwaway offline identity: username only matters for the author column
p=Path(sys.argv[1]); p.parent.mkdir(parents=True, exist_ok=True)
p.write_text(json.dumps({"username":"oracle-agent","alg":"ed25519","private_key":base64.b64encode(os.urandom(32)).decode(),"public_key":base64.b64encode(os.urandom(32)).decode()}))
PY
fi
rm -rf "$DATA/spool" "$DATA/daemon.log" "$DATA/hooks.log"; rm -f "$DATA"/counter-* "$DATA"/daemon-*.pid 2>/dev/null || true
echo "store: ${STATEFS_AI_STORE:-statefs.io at $STATEFS_DIRECTORY}"
claude -p "Run the shell command echo m1-oracle, then reply with one sentence describing what you did." \
  --plugin-dir ./plugin --model haiku --max-turns 3 --allowedTools "Bash(echo:*)" --output-format text >/dev/null
for _ in $(seq 1 60); do pgrep -f "statefs-ai daemon" >/dev/null || break; sleep 1; done
SPOOL=$(ls "$DATA"/spool/*.jsonl | head -1)
TRANSCRIPT=$(python3 -c "import json,sys
for l in open(sys.argv[1]):
    d=json.loads(l)
    if d['kind']=='session.start': print(d['content']['transcript_path']); break" "$SPOOL")
AUTHOR=$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['username'])" "$STATEFS_KEY_FILE")
NAME="$AUTHOR/$(basename "$PWD")#1"
OUT=$(./plugin/bin/statefs-ai replay "$NAME" --diff "$TRANSCRIPT")
echo "$OUT"
echo "$OUT" | grep -q "diff: 0 missing, 0 duplicated"
LAST=$(echo "$OUT" | grep -E '^\s+[0-9]+ ' | tail -1 | awk '{print $2}')
[ "$LAST" = "session.end" ] || { echo "FAIL: last row is $LAST, not session.end"; exit 1; }
echo "M1 ORACLE PASS ($NAME)"
