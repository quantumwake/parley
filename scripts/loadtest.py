#!/usr/bin/env python3
"""N concurrent headless Claude Code agents, each one session, each busy for
T minutes, all captured by the installed parley plugin into their own
conversations on statefs.io.

  scripts/loadtest.py --agents 10 --minutes 5 [--interval 6] [--model haiku] [--workdir /tmp/statefs-ai-load]

Each agent runs `claude -p --input-format stream-json --output-format
stream-json` in its own directory (so conversation names differ) and gets a
new small task on stdin every --interval seconds until --minutes elapse;
then stdin closes, the session ends, and the plugin's daemon writes
session.end. At the end the script counts rows per conversation from
statefs.io with `parley replay` and prints a table.
"""
import argparse, json, os, random, shutil, subprocess, sys, threading, time
from pathlib import Path

TASKS = [
    "Write a two-sentence description of a fictional bird species named after the number {n}.",
    "Run the shell command: echo tick-{n}. Then say one sentence about what you did.",
    "Give a haiku about the letter {c}.",
    "List three made-up but plausible names for a microservice, numbered.",
    "In one sentence, explain what a write-ahead log is to a child.",
    "Run the shell command: echo agent-{a}-turn-{n}. Then reply with one word.",
]

def stream_user(text):
    return json.dumps({"type": "user", "message": {"role": "user", "content": [{"type": "text", "text": text}]}}) + "\n"

def run_agent(idx, args, results):
    wd = Path(args.workdir) / f"agent-{idx:02d}"
    wd.mkdir(parents=True, exist_ok=True)
    cmd = ["claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
           "--model", args.model, "--allowedTools", "Bash(echo:*)"]
    env = {k: v for k, v in os.environ.items() if not k.startswith("STATEFS_")}
    p = subprocess.Popen(cmd, cwd=wd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
    info = {"agent": idx, "session": None, "turns": 0, "errors": 0}
    results[idx] = info

    def reader():
        for line in p.stdout:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if d.get("type") == "system" and d.get("session_id"):
                info["session"] = d["session_id"]
            if d.get("type") == "result":
                info["turns"] += 1
                if d.get("is_error"):
                    info["errors"] += 1
    t = threading.Thread(target=reader, daemon=True)
    t.start()

    deadline = time.time() + args.minutes * 60
    n = 0
    while time.time() < deadline:
        n += 1
        task = random.choice(TASKS).format(n=n, a=idx, c=random.choice("KQZXJ"))
        try:
            p.stdin.write(stream_user(task)); p.stdin.flush()
        except BrokenPipeError:
            break
        time.sleep(args.interval)
    try:
        p.stdin.close()
    except Exception:
        pass
    try:
        p.wait(timeout=120)
    except subprocess.TimeoutExpired:
        p.kill()
    t.join(timeout=5)
    info["stderr"] = p.stderr.read()[-300:]

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--agents", type=int, default=10)
    ap.add_argument("--minutes", type=float, default=5)
    ap.add_argument("--interval", type=float, default=6, help="seconds between tasks per agent")
    ap.add_argument("--model", default="haiku")
    ap.add_argument("--workdir", default="/tmp/statefs-ai-load")
    ap.add_argument("--keep", action="store_true", help="keep work dirs")
    args = ap.parse_args()

    data = Path.home() / ".claude/plugins/data/statefs-ai-statefs-ai"
    binary = data / "bin/parley"
    if not binary.exists():
        sys.exit(f"plugin binary not found at {binary}; run one session first")

    print(f"{args.agents} agents x {args.minutes} min, a task every {args.interval}s, model {args.model}")
    start = time.time()
    results = {}
    threads = [threading.Thread(target=run_agent, args=(i, args, results)) for i in range(1, args.agents + 1)]
    for t in threads: t.start()
    for t in threads: t.join()
    wall = time.time() - start

    # let daemons finish (they wait for the transcript to settle, then exit)
    for _ in range(90):
        if subprocess.run(["pgrep", "-f", "parley daemon"], capture_output=True).returncode != 0:
            break
        time.sleep(1)

    print(f"\n{'agent':<8}{'turns':>6}{'errors':>7}  conversation{'':<34} rows  last")
    total_rows, found = 0, 0
    for idx in sorted(results):
        info = results[idx]
        name, rows, last = "(none)", "?", "?"
        if info["session"]:
            out = subprocess.run([str(binary), "find", f"session={info['session']}"], capture_output=True, text=True).stdout.split()
            if len(out) >= 3:
                name, rows = out[0], out[2]
                found += 1
                total_rows += int(rows)
                rep = subprocess.run([str(binary), "replay", out[1]], capture_output=True, text=True).stdout.strip().splitlines()
                if len(rep) > 1:
                    last = rep[-2].split()[1]
        print(f"{idx:<8}{info['turns']:>6}{info['errors']:>7}  {name:<46}{rows:>5}  {last}")
    print(f"\nwall {wall:.0f}s, {total_rows} rows across {found}/{args.agents} conversations (server-side count)")
    if not args.keep:
        shutil.rmtree(args.workdir, ignore_errors=True)

if __name__ == "__main__":
    main()
