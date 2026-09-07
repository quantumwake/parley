#!/usr/bin/env python3
"""A small multi-agent experiment over one shared conversation.

N agents, each its own statefs identity and its own parley state, all talking
through one channel: the first agent creates it and posts the task, every
agent joins, then each agent runs a Claude Code session that is nudged every
--interval seconds to read the channel (the plugin injects new posts at the
start of each turn) and answer through `parley post`.

Setup (once, needs a tenant admin):
  scripts/swarm.py enroll a https://directory.statefs.io/enroll#en_...   # one per agent name
  ...
Run:
  scripts/swarm.py run --agents a,b,c --minutes 5 --task "Design a CLI for X; agree on flags; b and c implement, a reviews"
  scripts/swarm.py run --agents a,b,c --channel swarm-1 ...             # reuse a channel

Identities live in ~/.statefs-ai/swarm/<name>/identity; each agent's parley
state (subscriptions, cursors, spool) in ~/.statefs-ai/swarm/<name>/state.
Grants: the script prints the `parley grant` lines a tenant admin must run
and waits until every agent can read the channel.
"""
import argparse, json, os, subprocess, sys, threading, time
from pathlib import Path

HOME = Path.home()
SWARM = HOME / ".statefs-ai" / "swarm"
PARLEY = os.environ.get("PARLEY", "parley")
DIRECTORY = os.environ.get("STATEFS_DIRECTORY", "https://directory.statefs.io")

ROLES = {
    "a": "coordinator: break the task into parts, assign them by name, keep everyone on track, and post the final report",
    "b": "implementer: take the parts assigned to you, do them, and report progress",
    "c": "implementer: take the parts assigned to you, do them, and report progress",
    "d": "reviewer: critique what others post, ask sharp questions, catch mistakes",
    "e": "scribe: keep a running summary; post it as a report every few turns",
}

def agent_env(name):
    d = SWARM / name
    env = {k: v for k, v in os.environ.items() if not k.startswith("STATEFS_")}
    env["STATEFS_KEY_FILE"] = str(d / "identity")
    env["STATEFS_AI_DATA"] = str(d / "state")
    env["STATEFS_DIRECTORY"] = DIRECTORY
    return env

def parley(name, *args, check=True):
    r = subprocess.run([PARLEY, *args], env=agent_env(name), capture_output=True, text=True)
    if check and r.returncode != 0:
        raise SystemExit(f"[{name}] parley {' '.join(args)}: {r.stderr.strip() or r.stdout.strip()}")
    return r.stdout.strip()

def whoami(name):
    out = parley(name, "whoami", check=False)
    for line in out.splitlines():
        if line.startswith("identity:"):
            return line.split(":", 1)[1].strip()
    return None

def cmd_enroll(args):
    d = SWARM / args.name
    (d / "state").mkdir(parents=True, exist_ok=True)
    out = subprocess.run([PARLEY, "enroll", args.url, "--out", str(d / "identity"), "--label", f"swarm-{args.name}", "--caps", "read,write"],
                         env=agent_env(args.name), capture_output=True, text=True)
    print(out.stdout.strip() or out.stderr.strip())
    if out.returncode != 0:
        sys.exit(out.returncode)

def stream_user(text):
    return json.dumps({"type": "user", "message": {"role": "user", "content": [{"type": "text", "text": text}]}}) + "\n"

def run_agent(name, args, channel, results, stop):
    d = SWARM / name / "work"
    d.mkdir(parents=True, exist_ok=True)
    env = agent_env(name)
    ident = whoami(name) or name
    role = ROLES.get(name, "participant: help with the task and report progress")
    cmd = ["claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
           "--model", args.model, "--allowedTools", f"Bash({PARLEY}*),Bash(parley*),Bash(echo:*)"]
    p = subprocess.Popen(cmd, cwd=d, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
    info = {"agent": name, "identity": ident, "turns": 0, "errors": 0}
    results[name] = info

    def reader():
        for line in p.stdout:
            try:
                e = json.loads(line)
            except ValueError:
                continue
            if e.get("type") == "result":
                info["turns"] += 1
                if e.get("is_error"):
                    info["errors"] += 1
    threading.Thread(target=reader, daemon=True).start()

    brief = (f"You are agent '{ident}', role: {role}. You are in a shared conversation named '{channel}' with other agents. "
             f"The ONLY way to talk to them is `parley post {channel} --kind question|answer|comment|report|status --text \"...\" [--to <identity>] [--reply-to <event id>]`. "
             f"New posts from the channel appear at the start of your turns under 'statefs.ai parley: new posts'. "
             f"Read them, answer anything addressed to you or your role, and keep posts short and concrete. "
             f"Post `status` when you start or finish something, `question` when you need a decision, `answer` with --reply-to when you answer one, and `report` for results. Do not repeat yourself.")
    p.stdin.write(stream_user(brief + f"\n\nThe task is in the channel. Run `parley read {channel} --from 0 --peek` once to read it all, then act.")); p.stdin.flush()
    deadline = time.time() + args.minutes * 60
    n = 0
    while time.time() < deadline and not stop.is_set():
        time.sleep(args.interval)
        n += 1
        try:
            p.stdin.write(stream_user(f"Turn {n}: check what others posted (it is above if there is anything new), respond where useful, do the next concrete piece of your part, and post a short status. If the task is complete, post a final report and say DONE."))
            p.stdin.flush()
        except BrokenPipeError:
            break
    try:
        p.stdin.close()
    except Exception:
        pass
    try:
        p.wait(timeout=120)
    except subprocess.TimeoutExpired:
        p.kill()
    info["stderr"] = p.stderr.read()[-400:]

def cmd_run(args):
    names = [n.strip() for n in args.agents.split(",") if n.strip()]
    for n in names:
        if not (SWARM / n / "identity").exists():
            sys.exit(f"agent {n} is not enrolled: scripts/swarm.py enroll {n} <enrollment url>")
    idents = {n: whoami(n) for n in names}
    for n, i in idents.items():
        if not i:
            sys.exit(f"agent {n}: identity does not exchange (parley whoami failed)")
    print("agents:", ", ".join(f"{n}={idents[n]}" for n in names))

    lead = names[0]
    channel = args.channel or f"swarm-{time.strftime('%m%d-%H%M')}"
    if not args.channel:
        print(parley(lead, "create", channel, "--description", f"swarm experiment: {args.task[:60]}", "--tags", "swarm"))
    # grants: admin work; wait until everyone can read
    need = [n for n in names[1:] if "no read access" in parley(n, "join", channel, check=False) or "refused" in parley(n, "read", channel, "--peek", "--from", "0", check=False).lower()]
    if need:
        print("\nA tenant admin must grant these before the run can start:")
        for n in need:
            print(f"  parley grant {channel} --user {idents[n]} --access read,write")
        print("waiting (Ctrl-C to abort) ...")
        while need:
            time.sleep(5)
            need = [n for n in need if "refused" in parley(n, "read", channel, "--peek", "--from", "0", check=False).lower()]
    for n in names:
        parley(n, "join", channel, "--mode", "full", check=False)
    print(parley(lead, "post", channel, "--kind", "question", "--to", "*", "--text", f"TASK: {args.task}"))

    results, stop = {}, threading.Event()
    threads = [threading.Thread(target=run_agent, args=(n, args, channel, results, stop)) for n in names]
    for t in threads:
        t.start()
    try:
        for t in threads:
            t.join()
    except KeyboardInterrupt:
        stop.set()
        for t in threads:
            t.join(timeout=5)

    print(f"\n{'agent':<6}{'identity':<14}{'turns':>6}{'errors':>7}")
    for n in names:
        i = results.get(n, {})
        print(f"{n:<6}{i.get('identity',''):<14}{i.get('turns',0):>6}{i.get('errors',0):>7}")
    print(f"\nchannel: {channel}\n  parley replay {channel}\n  parley console")
    print(parley(lead, "replay", channel).splitlines()[-1])

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    e = sub.add_parser("enroll"); e.add_argument("name"); e.add_argument("url"); e.set_defaults(fn=cmd_enroll)
    r = sub.add_parser("run")
    r.add_argument("--agents", default="a,b,c"); r.add_argument("--minutes", type=float, default=5); r.add_argument("--interval", type=float, default=20)
    r.add_argument("--model", default="haiku"); r.add_argument("--task", required=True); r.add_argument("--channel", default="")
    r.set_defaults(fn=cmd_run)
    args = ap.parse_args()
    args.fn(args)

if __name__ == "__main__":
    main()
