#!/usr/bin/env python3
"""N Claude Code agents, each its own statefs identity, talking through one
shared conversation.

How it works:
  - Every agent is a `claude -p` session (stream-json in/out, model haiku by
    default) whose only tools are `parley ...` and `echo`.
  - Every agent acts as its own identity: the script sets STATEFS_KEY_FILE to
    ~/.statefs/identities/<name>/identity and STATEFS_AI_DATA to
    ~/.statefs-ai/swarm/<name>/state (its own subscriptions, cursors, spool)
    for that process. The machine's default identity is never touched.
  - The first agent creates the channel (it owns it), grants the others
    read,write with its own key (statefs `own` capability), and posts the task.
    Every agent joins in full mode.
  - --drive self (default): each agent gets the task once and runs until DONE;
    it waits for the others inside its own turn with
    `parley read <channel> --wait 90s`. The driver only nudges an agent whose
    turn ended without DONE when the channel moved since it last looked, or
    after 3 x --interval idle. The first agent is the closer; nobody else
    posts completion messages.
    --drive turns: the old pacing, a prompt every --interval seconds.
  - The summary lists turns, nudges, errors, failed tool calls (the command
    and the error it returned), and process exits.

Setup (once per agent name; the URL comes from the org):
  make swarm-enroll NAME=swarm-agent-test-1 URL='<enrollment url>'
  parley whoami --identity swarm-agent-test-1        # expect caps: read,write,own

Run:
  make test-swarm AGENTS=swarm-agent-test-1,swarm-agent-test-2,swarm-agent-test-3 MINUTES=5 EXAMPLE=parallel-docs
  make test-swarm AGENTS=a,b TASK="Design a CLI for X; a proposes, b critiques once, both agree"
  scripts/swarm.py run --agents a,b,c --channel swarm-1 --example debate   # reuse a channel
  scripts/swarm.py run --help                                              # all flags and examples

Afterwards:
  parley replay <channel>     or     parley console
"""
import argparse, json, os, shutil, subprocess, sys, threading, time
from pathlib import Path

HOME = Path.home()
IDENTITIES = HOME / ".statefs" / "identities"   # extra identities, beside the SDK's ~/.statefs/identity
SWARM = HOME / ".statefs-ai" / "swarm"           # parley state per agent
PARLEY = os.environ.get("PARLEY", "parley")
PARLEY_BIN = shutil.which(PARLEY) or PARLEY   # MCP needs an absolute command
DIRECTORY = os.environ.get("STATEFS_DIRECTORY", "")

# Roles by position in --agents: the first agent coordinates, the last reviews.
ROLES = [
    "coordinator: break the task into parts, assign them by identity name, keep everyone on track, and post the final report",
    "implementer: take the parts assigned to you, do them, and report progress",
    "implementer: take the parts assigned to you, do them, and report progress",
    "reviewer: critique what others post, ask sharp questions, catch mistakes",
    "scribe: keep a running summary; post it as a report every few turns",
]

EXAMPLES = {
    "naming": "Agree on a name and a one-paragraph description for a tiny CLI that keeps personal notes. "
              "The first agent proposes, the second critiques once, then both post the agreed result as a report.",
    "parallel-docs": "Each agent writes ONE section of a short design note for a personal notes CLI, on its own, then posts it as a report: "
                     "agent 1 the storage format, agent 2 the command set, agent 3 the sync story, further agents the test plan. "
                     "Do your own section first. Only after posting it, read the others' sections and post at most one comment on each.",
    "code-review": "Agent 1 posts a 20-line Python function that parses a note file (title line, blank line, body) as a report. "
                   "Every other agent reviews it once and posts findings as answers with --reply-to. Agent 1 posts a revised version and says what changed.",
    "estimation": "Each agent independently estimates, in hours, how long it would take to build a notes CLI with add, list, search and delete, "
                  "posts the number with a two-line rationale as a report, and does NOT wait for anyone. When all estimates are in, "
                  "the first agent posts the median and the spread as the final report.",
    "debate": "Should a personal notes CLI store notes as one file per note or one append-only log? Odd-numbered agents argue per-file, "
              "even-numbered argue the log. Each posts one argument, then one rebuttal to a specific post with --reply-to, then stops. "
              "The first agent posts a two-line verdict at the end.",
}

def agent_env(name):
    d = SWARM / name
    env = {k: v for k, v in os.environ.items() if not k.startswith("STATEFS_")}
    env["STATEFS_KEY_FILE"] = str(IDENTITIES / name / "identity")
    env["STATEFS_AI_DATA"] = str(d / "state")
    env["STATEFS_DIRECTORY"] = DIRECTORY
    return env

def parley(name, *args, check=True):
    r = subprocess.run([PARLEY, *args], env=agent_env(name), capture_output=True, text=True)
    if check and r.returncode != 0:
        raise SystemExit(f"[{name}] parley {' '.join(args)}: {r.stderr.strip() or r.stdout.strip()}")
    return r.stdout.strip()

def channel_head(name, channel):
    """Rows on the channel, from the agent's own view (0 on error)."""
    r = subprocess.run([PARLEY, "read", channel, "--from", "0", "--peek"], env=agent_env(name), capture_output=True, text=True)
    for line in r.stdout.splitlines()[::-1]:
        if line.endswith("new rows"):
            try:
                return int(line.split()[0])
            except ValueError:
                return 0
    return 0

def can_read(name, channel):
    r = subprocess.run([PARLEY, "read", channel, "--from", "0", "--peek"], env=agent_env(name), capture_output=True, text=True)
    return r.returncode == 0

def whoami(name):
    out = parley(name, "whoami", check=False)
    for line in out.splitlines():
        if line.startswith("identity:"):
            return line.split(":", 1)[1].strip()
    return None

def cmd_enroll(args):
    (SWARM / args.name / "state").mkdir(parents=True, exist_ok=True)
    (IDENTITIES / args.name).mkdir(parents=True, exist_ok=True)
    out = subprocess.run([PARLEY, "enroll", args.url, "--out", str(IDENTITIES / args.name / "identity"), "--label", f"swarm-{args.name}"],
                         env=agent_env(args.name), capture_output=True, text=True)
    print(out.stdout.strip() or out.stderr.strip())
    if out.returncode != 0:
        sys.exit(out.returncode)

def stream_user(text):
    return json.dumps({"type": "user", "message": {"role": "user", "content": [{"type": "text", "text": text}]}}) + "\n"

def run_agent(name, args, channel, results, stop, role):
    roster_names = [n for n, _ in getattr(args, "roster", [])]
    closer_name = whoami(roster_names[0]) or roster_names[0] if roster_names else name
    closer = roster_names and name == roster_names[0]
    d = SWARM / name / "work"
    d.mkdir(parents=True, exist_ok=True)
    env = agent_env(name)
    ident = whoami(name) or name
    # Agents call parley through its MCP server, not a shell: structured
    # arguments have no quoting layer, so a multi-line markdown report posts
    # as written. The server inherits this agent's STATEFS_* env, so it acts
    # as that agent's identity. Bash stays allowed as a fallback.
    mcp_cfg = SWARM / name / "mcp.json"
    mcp_cfg.write_text(json.dumps({"mcpServers": {"parley": {"command": PARLEY_BIN, "args": ["mcp"]}}}))
    tools = ",".join("mcp__parley__" + t for t in
                     ("search_conversations", "join_conversation", "leave_conversation", "post_message",
                      "read_conversation", "my_subscriptions", "create_conversation", "grant_access", "whoami"))
    cmd = ["claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
           "--mcp-config", str(mcp_cfg), "--model", args.model,
           "--allowedTools", f"{tools},Bash({PARLEY}*),Bash(parley*),Bash(echo:*),Write"]
    p = subprocess.Popen(cmd, cwd=d, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
    info = {"agent": name, "identity": ident, "turns": 0, "errors": 0}
    results[name] = info

    turn_over, done = threading.Event(), threading.Event()
    cmds = {}

    def reader():
        for line in p.stdout:
            try:
                e = json.loads(line)
            except ValueError:
                continue
            t = e.get("type")
            if t == "assistant":
                for blk in (e.get("message") or {}).get("content") or []:
                    if isinstance(blk, dict) and blk.get("type") == "tool_use":
                        cmds[blk.get("id")] = (blk.get("input") or {}).get("command") or blk.get("name")
            if t == "result":
                info["turns"] += 1
                info["last_turn_end"] = time.time()
                if e.get("is_error"):
                    info["errors"] += 1
                if "DONE" in str(e.get("result", "")) or "TASK COMPLETE" in str(e.get("result", "")).upper():
                    done.set()
                turn_over.set()
            elif t == "user":  # tool results come back as user messages: record failures with their error
                for blk in (e.get("message") or {}).get("content") or []:
                    if isinstance(blk, dict) and blk.get("type") == "tool_result" and blk.get("is_error"):
                        info["tool_errors"] = info.get("tool_errors", 0) + 1
                        why = blk.get("content")
                        if isinstance(why, list):
                            why = " ".join(x.get("text", "") for x in why if isinstance(x, dict))
                        info.setdefault("failed", []).append((
                            str(cmds.get(blk.get("tool_use_id"), "?")).replace("\n", " ")[:100],
                            str(why or "").replace("\n", " ")[:160]))
    threading.Thread(target=reader, daemon=True).start()

    roster = ", ".join(f"'{n}' ({r.split(':')[0]})" for n, r in getattr(args, "roster", []))
    brief = (f"You are agent '{ident}', role: {role}. You are in a shared conversation named '{channel}' with exactly these agents: {roster}. "
             f"Nobody else is present; address only these identities. "
             f"When your part is done, say DONE to me (not on the channel) and stop; do not post 'complete' or 'done' status messages. "
             f"{'You are the closer: when everyone has delivered, post ONE final report and say DONE.' if closer else 'Only ' + closer_name + ' posts the final report.'} "
             f"Talk to them with the parley tools: post_message (name, text, kind, to, reply_to) to speak, read_conversation (name, wait_seconds) to listen. "
             f"Text may be full markdown, as long as you like: it is a tool argument, not a shell command. "

             f"New posts appear at the start of your turns, and read_conversation with wait_seconds 90 blocks until someone posts and returns what arrived. "
             f"Work until the task is done: when you need something from another agent, post it, then wait with read_conversation and continue in the same turn; do not end your turn just because you are waiting. "
             f"Read them, answer anything addressed to you or your role, and keep posts short and concrete. "
             f"Post `status` when you start or finish something, `question` when you need a decision, `answer` with --reply-to when you answer one, and `report` for results. Do not repeat yourself.")
    p.stdin.write(stream_user(brief + f"\n\nThe task is in the channel. Run `parley read {channel} --from 0 --peek` once to read it all, then act.")); p.stdin.flush()
    deadline = time.time() + args.minutes * 60
    n = 0
    if args.drive == "self":
        # Hand the task over once, then supervise: nudge only when the agent's
        # turn has ended without DONE and either the channel moved since it
        # last looked or it has been idle for a while.
        seen = channel_head(name, channel)
        idle_since = time.time()
        while time.time() < deadline and not stop.is_set() and not done.is_set():
            if p.poll() is not None:
                info["exited"] = p.returncode
                break
            if not turn_over.wait(timeout=5):
                continue
            head = channel_head(name, channel)
            moved, idle = head > seen, time.time() - idle_since
            if moved or idle >= args.interval * 3:
                turn_over.clear()
                seen, idle_since = head, time.time()
                n += 1
                why = "New posts arrived on the channel (shown above)." if moved else "Nothing new arrived for a while."
                try:
                    p.stdin.write(stream_user(f"{why} Continue your part of the task; if you are waiting on someone, wait with read_conversation (wait_seconds 90) instead of stopping. If the task is complete, post the final report and say DONE."))
                    p.stdin.flush()
                except BrokenPipeError:
                    break
        info["nudges"] = n
    while args.drive == "turns" and time.time() < deadline and not stop.is_set():
        time.sleep(args.interval)
        n += 1
        try:
            p.stdin.write(stream_user(
                f"Turn {n}: if others posted something new it is above. Answer anything addressed to you. Then do the next concrete piece of YOUR part. "
                f"Post only when you have a result, a question, or a decision; do not post a status just to say you are waiting, and do not ask someone for an update you already asked for. "
                f"If you are blocked on another agent, work on something else or simply reply 'waiting' to me here (not on the channel). If the task is complete, post a final report and say DONE."))
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
    if args.example:
        args.task = EXAMPLES[args.example]
    if not args.task:
        sys.exit("--task \"...\" or --example " + "|".join(sorted(EXAMPLES)) + " is required")
    names = [n.strip() for n in args.agents.split(",") if n.strip()]
    for n in names:
        if not (IDENTITIES / n / "identity").exists():
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
    need = [n for n in names if not can_read(n, channel)]
    if need:
        # The owner shares its own channel when its key carries `own`
        # (statefs PERMISSIONS.md §5); otherwise a tenant admin must.
        granted = []
        for n in need:
            r = subprocess.run([PARLEY, "grant", channel, "--user", idents[n], "--access", "read,write"], env=agent_env(lead), capture_output=True, text=True)
            if r.returncode == 0:
                granted.append(n)
        need = [n for n in need if n not in granted and not can_read(n, channel)]
    if need:
        print("\nThe channel owner's key cannot share (no `own` capability, or the directory predates owner grants); a tenant admin must grant:")
        for n in need:
            print(f"  parley grant {channel} --user {idents[n]} --access read,write --identity ~/.statefs/identities/admin/identity")
        print("waiting for the grants (Ctrl-C to abort) ...")
        while need:
            time.sleep(5)
            need = [n for n in need if not can_read(n, channel)]
        print("all agents can read the channel")
    roles = [ROLES[i] if i < len(ROLES) else "participant: help with the task and report progress" for i in range(len(names))]
    args.roster = list(zip(names, roles))
    for i, n in enumerate(names):
        # Each agent speaks under a handle: its role. Several sessions can
        # share one identity, so the handle is what tells them apart in the
        # transcript; the identity still says who holds the write grant.
        parley(n, "join", channel, "--mode", "full", "--as", roles[i].split(":")[0], check=False)
    print(parley(lead, "post", channel, "--kind", "question", "--to", "*", "--text", f"TASK: {args.task}"))

    results, stop = {}, threading.Event()
    threads = [threading.Thread(target=run_agent, args=(n, args, channel, results, stop, roles[i])) for i, n in enumerate(names)]
    for t in threads:
        t.start()
    try:
        for t in threads:
            t.join()
    except KeyboardInterrupt:
        stop.set()
        for t in threads:
            t.join(timeout=5)

    w = max(6, max(len(n) for n in results) + 2)
    print(f"\n{'agent':<{w}}{'identity':<{w}}{'turns':>6}{'nudges':>7}{'errors':>7}{'tool-errs':>10}{'exit':>6}")
    for n in names:
        i = results.get(n, {})
        print(f"{n:<{w}}{i.get('identity',''):<{w}}{i.get('turns',0):>6}{i.get('nudges',0):>7}{i.get('errors',0):>7}{i.get('tool_errors',0):>10}{str(i.get('exited','')):>6}")
    for n, i in results.items():
        for cmd, why in i.get("failed", []):
            print(f"  tool error [{n}]: {cmd}\n      -> {why}")
    print(f"\nchannel: {channel}\n  parley replay {channel}\n  parley console")
    print(parley(lead, "replay", channel).splitlines()[-1])

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    e = sub.add_parser("enroll"); e.add_argument("name"); e.add_argument("url"); e.set_defaults(fn=cmd_enroll)
    r = sub.add_parser("run")
    r.add_argument("--agents", default="a,b,c"); r.add_argument("--minutes", type=float, default=5); r.add_argument("--interval", type=float, default=20)
    r.add_argument("--model", default="haiku"); r.add_argument("--task", default=""); r.add_argument("--channel", default="")
    r.add_argument("--example", default="", choices=sorted(EXAMPLES), help="a canned task: " + ", ".join(sorted(EXAMPLES)))
    r.add_argument("--drive", default="self", choices=["self", "turns"], help="self: hand the task over once and only nudge when stuck (default); turns: prompt every --interval seconds")
    r.set_defaults(fn=cmd_run)
    args = ap.parse_args()
    args.fn(args)

if __name__ == "__main__":
    main()
