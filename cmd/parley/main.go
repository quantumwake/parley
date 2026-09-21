// parley is the plugin binary: Claude Code hooks call `parley hook`,
// people call `parley enroll <url>` and `parley whoami`, and
// `parley fakedir` runs the test directory for local spikes.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/quantumwake/statefs/pkg/identityfile"

	"github.com/quantumwake/parley/pkg/console"
	"github.com/quantumwake/parley/pkg/enroll"
	"github.com/quantumwake/parley/pkg/mcp"
	"github.com/quantumwake/parley/pkg/plugin"
)

//go:embed VERSION
var version string

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
		usage()
		os.Exit(0)
	}

	plugin.ClientVersion = strings.TrimSpace(version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "setup":
		err = cmdSetup(ctx, os.Args[2:])
	case "hook":
		env := plugin.EnvFromProcess()
		env.HookEvent = hookEventArg(os.Args[2:])
		err = plugin.Handle(ctx, env, os.Stdin, os.Stdout)
	case "enroll":
		err = cmdEnroll(ctx, os.Args[2:])
	case "whoami":
		err = cmdWhoami(ctx, os.Args[2:])
	case "identity", "identities":
		err = cmdIdentity(ctx, os.Args[2:])
	case "status":
		err = cmdStatus(ctx)
	case "config":
		err = cmdConfig(os.Args[2:])
	case "install-path":
		err = cmdInstallPath(os.Args[2:])
	case "console":
		err = cmdConsole(ctx, os.Args[2:])
	case "find":
		err = cmdFind(ctx, os.Args[2:])
	case "sessions":
		err = cmdSessions(ctx, os.Args[2:])
	case "conversation":
		err = cmdConversation(ctx, os.Args[2:])
	case "create", "list", "join", "leave", "subscriptions", "post", "read", "grant":
		err = cmdConversation(ctx, os.Args[1:])
	case "delete":
		err = cmdDelete(ctx, os.Args[2:])
	case "describe":
		err = cmdDescribe(ctx, os.Args[2:])
	case "daemon":
		err = cmdDaemon(ctx, os.Args[2:])
	case "replay":
		err = cmdReplay(ctx, os.Args[2:])
	case "cleanup-conformance":
		err = cmdCleanup(ctx, os.Args[2:])
	case "fakedir":
		err = cmdFakeDir(ctx, os.Args[2:])
	case "labels":
		err = cmdLabels(ctx, os.Args[2:])
	case "wait":
		err = cmdWait(ctx, os.Args[2:])
	case "work":
		err = cmdWork(ctx, os.Args[2:])
	case "statusline":
		err = cmdStatusLine()
	case "mcp":
		err = cmdMCP(ctx)
	case "version":
		err = cmdVersion(ctx, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "parley:", err)
		switch {
		case errors.Is(err, enroll.ErrTokenRejected), errors.Is(err, enroll.ErrAlreadyEnrolled):
			os.Exit(3)
		}

		os.Exit(4)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `parley `+strings.TrimSpace(version)+`  (statefs.ai parley: agent sessions and org channels)

Once this machine is enrolled in a statefs.ai organization, sessions are
recorded as conversations that identity can access: org channels, teams,
what it owns and shares, and private agent records.

SETUP
  parley setup [auto|claude|antigravity|grok|codex]
                                register parley with a local agent CLI
                                  auto detects whichever of those is present
                                  grok: MCP only (grok mcp add)
                                  antigravity/codex: MCP plus host hooks
  parley enroll <url|token>     enroll this machine with a URL minted by your
                                statefs.ai organization (single use)
                                  --caps read,write  --out PATH  --reset  --label L  --default
                                  (own: title, describe, share and delete what this identity creates)
  parley status                 enrollment, directory, and conversations recorded here
  parley config                 where parley is enrolled, and why
                                  --directory URL  --statefs-ai URL  set them ("" resets to the default;
                                  STATEFS_DIRECTORY / STATEFS_AI_APP still win)
                                  --gate NAME=COMMAND  judge posts that would interrupt this session with
                                  a command (the post as JSON on stdin, {"verdict":"react|context|display|
                                  ignore"} on stdout); it may only quiet a post, never raise one. None by
                                  default.   --no-gates  --gate-timeout-ms N
  parley statusline             one line of counts per conversation for settings.json statusLine
  parley whoami                 prove the identity can log in
  parley identity list          the identities on this machine and which one parley acts as
                                  --verify also logs each in and shows its caps
  parley identity use <name>    act as this identity
                                  --session  this Claude/Grok session only
                                  --project  this working directory (.parley-identity)
                                  (neither: machine default)   --tenant T
  parley identity remove <name> delete the local key file (not the server enrollment)
                                  type the identity username to confirm, or pass --force --yes
  parley install-path [--dir D] link parley into a directory on your PATH
  parley labels [--limit N]     which scope labels exist and their values, so you know what
                                you can filter on before searching
  parley mcp                    serve these conversation operations to an MCP client over
                                stdio, so an agent calls them as tools instead of shell
                                commands (the plugin starts this for you)

VIEW
  parley console [--listen 127.0.0.1:0] [--no-open]
                                open the conversation viewer in your browser: your recorded
                                sessions and shared conversations as chat, live, with a composer

YOUR RECORDED SESSIONS
  parley sessions [--limit N]   this identity's recorded sessions, newest first, each with the
                                claude --resume line when its transcript is on this machine
                                (otherwise it says why it cannot be resumed from here)
  parley find [k=v ...]         conversations this identity can see, by label
                                  e.g.  parley find agent=<me>   parley find session=<id>
                                  --heads also reads each conversation's row count (slower)
  parley describe [name|id] --title T [--description D] [--tags a,b]
                                title and describe a conversation (no target = the session
                                recording now); labels the namespace and logs a meta.purpose row
  parley replay <name|id>       print a conversation in order
                                  --from N  --json  --diff <transcript.jsonl>  (checks nothing is missing)

SHARED CONVERSATIONS (channels your tenant can find)
  parley list [--tag T] [--q X] shared conversations in your tenant; access says owner,
                                admin, tenant (open to all members) or grant? (ask an admin)
  parley create <name>          start one   --description "..."  --tags a,b
  parley join <name>            follow it: new posts are injected at the start of your turns
                                  --mode full|digest   --pick all|first|<persona>   (cap: 20)
  parley leave <name>           stop following
  parley subscriptions          what you follow, with read cursors
  parley post <name> --text T   say something   --kind question|answer|comment|report|status (exchange)
                                or work: --kind request | claim [--reply-to <request>] | close --reply-to <claim> --outcome resolved|handed_over|dropped
  parley work [name...] [--all] open and claimed work in the conversations followed (--all includes closed)
                                  --to <user>  --reply-to <event id>  --tags a,b
  parley read <name>            catch up from your cursor   --from N   --peek (keep the cursor)   --wait 90s (block until someone else posts)
  parley wait [name...]         block until a followed conversation has a post from someone else, print it, exit;
                                exits with an error when the directory cannot be read, and after 60m asks to be re-armed
                                (run it as a background task: its exit wakes an idle agent)   --timeout 50m
  parley grant <name> --user U  share a conversation you own   --access read|write|read,write
                                (needs the own capability; a tenant admin with manage can share any)
                                --identity PATH   act as another local identity for this call
  parley delete <name|id>       remove a conversation you own (own capability; admins with manage: any)
                                --identity PATH   act as another local identity

INTERNAL (called by the Claude Code plugin)
  parley hook                   reads a hook event on stdin
  parley daemon --session ID --transcript PATH [--cwd DIR]
  parley fakedir [--listen :8477] [--username U]     local fake directory for tests (never production)
  parley cleanup-conformance [--dry-run]             delete purpose:conformance namespaces only
  parley version

EXAMPLES
  parley enroll '<enrollment url>'                             first time on this machine
  parley status                                                 am I enrolled, what was recorded
  parley describe --title "Abyss Dive" --description "canvas arcade game built from one prompt"
  parley console                                                open the viewer in the browser
  parley list --tag ci                                          shared conversations tagged ci
  parley create platform --description "platform team" --tags ci,infra
  parley join platform --mode digest                            follow, reports and summaries only
  parley post platform --kind question --text "who owns the migrate race?" --to '*'
  parley post platform --kind answer --reply-to 01M1WRYRM3JGW3N6SM3R9G236C --text "me"
  parley read platform                                          catch up from where I left off
  parley read swarm-notes --wait 90s                            wait up to 90 s for someone to post, then print it
  parley find agent=kas-agent-2 --heads                         my recorded sessions with row counts
  parley replay 'kas-agent-2/statefs.ai#ecd9b945' --diff ~/.claude/projects/.../<session>.jsonl
  parley delete parley-test-2002 --identity manage

Directory: STATEFS_DIRECTORY, else ~/.statefs-ai/config.json, else the enrolled default
Identity:  STATEFS_KEY_FILE, else the config, else ~/.statefs/identity
`)
}

func hookEventArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--event" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func cmdEnroll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	directory := fs.String("directory", plugin.EnvFromProcess().Directory, "directory base URL when the token is bare (default: the config from enroll)")
	label := fs.String("label", "", "label for the registered key (default statefs-ai@hostname)")
	out := fs.String("out", os.Getenv("STATEFS_KEY_FILE"), "identity file path (default ~/.statefs/identity)")
	reset := fs.Bool("reset", false, "replace an existing identity file")
	makeDefault := fs.Bool("default", false, "make this identity the machine's default for hooks and commands (automatic when --out is the default path or no default exists yet)")
	tenant := fs.String("tenant", os.Getenv("STATEFS_TENANT"), "acting tenant for the verification exchange")
	caps := fs.String("caps", "", "narrow this key to a subset of what the token grants, e.g. read,write (default: everything the token grants; asking for more burns the token)")
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}

		positional = append(positional, a)
	}

	if err := fs.Parse(args[len(positional):]); err != nil {
		return err
	}

	if len(positional) != 1 {
		return errors.New("enroll needs exactly one URL or token")
	}

	req, err := enroll.ParseURL(positional[0])
	if err != nil {
		return err
	}

	if req.Directory == "" {
		req.Directory = strings.TrimRight(*directory, "/")
	}

	res, err := enroll.Enroll(ctx, req, enroll.Options{Label: *label, Path: *out, Reset: *reset, Caps: splitCaps(*caps)})
	if err != nil {
		return err
	}

	fmt.Printf("enrolled: %s\nidentity file: %s\npublic key: %s\n", res.Username, res.Path, res.PublicKey)
	st, err := enroll.Verify(ctx, res.Directory, res.Path, *tenant)
	if err != nil {
		return fmt.Errorf("enrolled but the token exchange failed: %w", err)
	}

	fmt.Printf("verified: acting token issued for %s at %s\n", st.Username, st.Directory)
	// The default identity is the one hooks and plain commands use. An
	// enrollment into another path (a swarm agent, a manage key) must not
	// hijack it: only the default path, a missing default, or --default
	// writes the config.
	cur := plugin.LoadConfig()
	isDefaultPath := res.Path == identityfile.DefaultPath()
	if *makeDefault || isDefaultPath || cur.Identity == "" {
		// A link from an installation's portal names its statefs.ai; keep
		// the one already set when this link names none.
		ai := req.StatefsAI
		if ai == "" {
			ai = cur.StatefsAI
		}
		if err := plugin.SaveConfig(plugin.Config{Directory: res.Directory, Identity: res.Path, Tenant: *tenant, StatefsAI: ai}); err != nil {
			return fmt.Errorf("config: %w", err)
		}

		fmt.Printf("default identity for this machine: %s (%s)\n", res.Username, plugin.ConfigPath())
	} else {
		fmt.Printf("extra identity (the machine's default stays %s); use it with STATEFS_KEY_FILE=%s, or re-run with --default\n", filepath.Base(filepath.Dir(cur.Identity))+"/"+filepath.Base(cur.Identity), res.Path)
	}

	return nil
}

func cmdWhoami(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	env := plugin.EnvFromProcess()
	directory := fs.String("directory", env.Directory, "directory base URL")
	path := fs.String("identity", env.IdentityPath, "identity file path, or the name of an identity under ~/.statefs/identities")
	tenant := fs.String("tenant", env.Tenant, "acting tenant")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := enroll.Verify(ctx, strings.TrimRight(*directory, "/"), plugin.IdentityArg(*path), *tenant)
	if err != nil {
		return err
	}

	cl := plugin.MyClaims(ctx, plugin.Env{Directory: st.Directory, IdentityPath: st.Path, Tenant: *tenant})
	fmt.Printf("identity: %s\nfile: %s\ndirectory: %s\nexchange: ok\ncaps: %s\nadmin: %v\n", st.Username, st.Path, st.Directory, strings.Join(cl.Caps, ","), cl.IsAdmin)
	return nil
}

func cmdIdentity(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}

	env := plugin.EnvFromProcess()
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("identity list", flag.ContinueOnError)
		verify := fs.Bool("verify", false, "log each identity in and show its caps")
		if err := fs.Parse(args); err != nil {
			return err
		}

		ids := plugin.Identities(env.IdentityPath)
		if len(ids) == 0 {
			fmt.Println("no identities on this machine; enroll one with `parley enroll <url>`")
			return nil
		}

		for _, id := range ids {
			mark := " "
			if id.Current {
				mark = "*"
			}

			line := fmt.Sprintf("%s %-24s %-28s %s", mark, id.Name, id.Username, id.Directory)
			if *verify {
				cl := plugin.MyClaims(ctx, env.WithIdentity(id))
				if cl.Sub == "" {
					line += "  (login failed)"
				} else {
					line += "  caps " + strings.Join(cl.Caps, ",")
					if cl.IsAdmin {
						line += " admin"
					}
				}
			}

			fmt.Println(strings.TrimRight(line, " "))
		}

		if os.Getenv("STATEFS_KEY_FILE") != "" {
			fmt.Println("(* is STATEFS_KEY_FILE, which overrides the configured identity)")
		}

		return nil
	case "use":
		var positional []string
		for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			positional, args = append(positional, args[0]), args[1:]
		}

		fs := flag.NewFlagSet("identity use", flag.ContinueOnError)
		tenant := fs.String("tenant", "", "acting tenant for this identity (default: none)")
		session := fs.Bool("session", false, "this session only (not the machine default)")
		sessionID := fs.String("session-id", plugin.SessionFromEnv(), "session id for --session")
		project := fs.Bool("project", false, "this working directory (.parley-identity); works in Claude, Grok, Codex, Antigravity")
		if err := fs.Parse(args); err != nil {
			return err
		}

		if len(positional) != 1 {
			return errors.New("identity use needs one name or path (parley identity list)")
		}

		id, err := plugin.ResolveIdentity(positional[0])
		if err != nil {
			return err
		}

		if *session {
			sid := *sessionID
			if sid == "" {
				sid = plugin.SessionFromEnv()
			}
			if sid == "" {
				return errors.New("identity use --session: no session id (Claude, Grok, or PARLEY_SESSION; or pass --session-id, or use --project)")
			}
			if err := plugin.PinSessionIdentity(env, sid, positional[0]); err != nil {
				return err
			}
			fmt.Printf("this session acts as %s (%s)\n", id.Username, id.Name)
		}
		if *project {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := plugin.PinProjectIdentity(cwd, positional[0]); err != nil {
				return err
			}
			fmt.Printf("this project acts as %s (%s)  (.parley-identity)\n", id.Username, id.Name)
		}
		if !*session && !*project {
			cfg, err := plugin.UseIdentity(id, *tenant)
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			fmt.Printf("default identity for this machine: %s (%s)\ndirectory: %s\n", id.Username, id.Name, cfg.Directory)
			fmt.Println("new sessions and commands act as it unless they pin --session or --project")
		}
		if os.Getenv("STATEFS_KEY_FILE") != "" {
			fmt.Println("note: STATEFS_KEY_FILE is set in this shell and still overrides pins")
		}

		return nil
	case "remove", "rm", "delete":
		var positional []string
		for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			positional, args = append(positional, args[0]), args[1:]
		}
		fs := flag.NewFlagSet("identity remove", flag.ContinueOnError)
		force := fs.Bool("force", false, "with --yes, skip typing the identity name")
		yes := fs.Bool("yes", false, "with --force, skip typing the identity name")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if len(positional) != 1 {
			return errors.New("identity remove needs one name or path (parley identity list)")
		}
		id, err := plugin.ResolveIdentity(positional[0])
		if err != nil {
			return err
		}
		if err := plugin.ConfirmIdentityRemoval(id, *force, *yes, os.Stdin, os.Stderr); err != nil {
			return err
		}
		if err := plugin.RemoveIdentity(id); err != nil {
			return err
		}
		fmt.Printf("removed local identity %s (%s)\n", id.Username, id.Path)
		return nil
	default:
		return fmt.Errorf("identity: unknown subcommand %q (list, use, remove)", sub)
	}
}

func cmdDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	session := fs.String("session", "", "client session id")
	transcript := fs.String("transcript", "", "transcript jsonl path")
	cwd := fs.String("cwd", "", "session working directory")
	idle := fs.Duration("idle", 4*time.Hour, "exit after this long without session.end")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *session == "" {
		return errors.New("--session is required")
	}

	return plugin.RunDaemon(ctx, plugin.EnvFromProcess(), plugin.DaemonOptions{SessionID: *session, TranscriptPath: *transcript, CWD: *cwd, IdleTimeout: *idle})
}

func cmdReplay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	from := fs.Int64("from", 0, "first position")
	asJSON := fs.Bool("json", false, "print rows as JSON lines")
	diff := fs.String("diff", "", "transcript to compare against")
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}

		positional = append(positional, a)
	}

	if err := fs.Parse(args[len(positional):]); err != nil {
		return err
	}

	if len(positional) != 1 {
		return errors.New("replay needs one namespace id or display name")
	}

	return plugin.Replay(ctx, plugin.EnvFromProcess(), positional[0], *from, *asJSON, *diff, os.Stdout)
}

func cmdCleanup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cleanup-conformance", flag.ContinueOnError)
	directory := fs.String("directory", plugin.EnvFromProcess().Directory, "directory base URL")
	dry := fs.Bool("dry-run", false, "list only")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return plugin.CleanupConformance(ctx, strings.TrimRight(*directory, "/"), *dry, os.Stdout)
}

func cmdFind(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	limit := fs.Int("limit", 100, "max results")
	heads := fs.Bool("heads", false, "also read each conversation's head (slower: a member read per row)")
	var pairs []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}

		pairs = append(pairs, a)
	}

	if err := fs.Parse(args[len(pairs):]); err != nil {
		return err
	}

	return plugin.Find(ctx, plugin.EnvFromProcess(), pairs, *limit, *heads, os.Stdout)
}

func cmdSessions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "max sessions")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return plugin.Sessions(ctx, plugin.EnvFromProcess(), plugin.ClaudeDir(), *limit, os.Stdout)
}

func cmdConversation(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("conversation needs a subcommand: create | list | join | leave | subscriptions | post | read | grant")
	}

	env := plugin.EnvFromProcess()
	sub, rest := args[0], args[1:]
	var positional []string
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			break
		}

		positional = append(positional, a)
	}

	flags := rest[len(positional):]
	name := ""
	if len(positional) > 0 {
		name = positional[0]
	}

	fs := flag.NewFlagSet("conversation "+sub, flag.ContinueOnError)
	description := fs.String("description", "", "one line")
	tags := fs.String("tags", "", "comma-separated")
	tag := fs.String("tag", "", "filter by one tag")
	q := fs.String("q", "", "substring of name or description")
	mode := fs.String("mode", "full", "full | digest")
	pick := fs.String("pick", "all", "digest pick: all | first | <persona>")
	text := fs.String("text", "", "post body")
	textFile := fs.String("text-file", "", "read the post body from a file (or - for stdin); use this for multi-line markdown, which the shell cannot quote safely")
	kind := fs.String("kind", "comment", "exchange: question | answer | comment | report | status | artifact; work: request | claim | close")
	outcome := fs.String("outcome", "", "close: resolved | handed_over | dropped")
	subject := fs.String("subject", "", "claim with no --reply-to: what you are working on (a path, branch or PR); a second claim on it is told who holds it")
	to := fs.String("to", "", "identity, or everyone for every subscriber")
	replyTo := fs.String("reply-to", "", "event id this answers")
	from := fs.Int64("from", -1, "first position (default: the subscription cursor)")
	peek := fs.Bool("peek", false, "do not advance the cursor")
	wait := fs.Duration("wait", 0, "block up to this long for at least one new row, e.g. 90s (poll every 2 s)")
	waitSecs := fs.Int("wait-seconds", 0, "same as --wait, in seconds (the MCP tool's spelling)")
	user := fs.String("user", "", "member username to grant")
	access := fs.String("access", "read", "read | write | read,write")
	as := fs.String("as", "", "join: the handle to speak under in this conversation (distinguishes sessions that share one identity)")
	identity := fs.String("identity", "", "identity file to act as, or a name under ~/.statefs/identities (default: the configured identity)")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	if *identity = plugin.IdentityArg(*identity); *identity != "" {
		env.IdentityPath = *identity // every subcommand acts as this identity
	}

	split := func(s string) []string {
		if s == "" {
			return nil
		}

		return strings.Split(s, ",")
	}
	switch sub {
	case "create":
		if name == "" {
			return errors.New("create needs a name")
		}

		return plugin.CreateShared(ctx, env, name, *description, split(*tags), os.Stdout)
	case "list":
		return plugin.ListShared(ctx, env, *tag, *q, os.Stdout)
	case "join":
		return plugin.Join(ctx, env, name, *mode, *pick, *as, os.Stdout)
	case "leave":
		return plugin.Leave(env, name, os.Stdout)
	case "subscriptions":
		return plugin.ShowSubscriptions(ctx, env, os.Stdout)
	case "post":
		body, err := postBody(*text, *textFile)
		if err != nil {
			return err
		}

		return plugin.Post(ctx, env, name, *kind, body, *to, *replyTo, split(*tags), os.Stdout, plugin.WithOutcome(*outcome), plugin.WithSubject(*subject))
	case "read":
		if *wait == 0 && *waitSecs > 0 {
			*wait = time.Duration(*waitSecs) * time.Second
		}

		return plugin.Read(ctx, env, name, *from, *peek, *wait, os.Stdout)
	case "grant":
		if *user == "" {
			return errors.New("grant needs --user")
		}

		return plugin.GrantAccess(ctx, env, name, *user, *access, os.Stdout)
	}

	return fmt.Errorf("unknown conversation subcommand %q", sub)
}

// cmdWait: parley wait [name...] [--timeout 60m]. Meant to run as a
// background task, whose exit is what wakes an idle agent.
func cmdWait(ctx context.Context, args []string) error {
	var names []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		names, args = append(names, args[0]), args[1:]
	}

	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	timeout := fs.Duration("timeout", plugin.WaitLifetime, "exit after this long asking to be re-armed (0: until a post or a failure); a lost directory always ends the wait")
	if err := fs.Parse(args); err != nil {
		return err
	}

	names = append(names, fs.Args()...)
	return plugin.Wait(ctx, plugin.EnvFromProcess(), names, *timeout, os.Stdout)
}

// cmdWork: parley work [name...] [--all].
func cmdWork(ctx context.Context, args []string) error {
	var names []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		names, args = append(names, args[0]), args[1:]
	}

	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	all := fs.Bool("all", false, "include closed work")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return plugin.ListWork(ctx, plugin.EnvFromProcess(), append(names, fs.Args()...), *all, os.Stdout)
}

func cmdConsole(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:0", "address to serve on")
	noOpen := fs.Bool("no-open", false, "do not open the browser")
	if err := fs.Parse(args); err != nil {
		return err
	}

	srv, err := console.New(ctx, plugin.EnvFromProcess(), consolePage())
	if err != nil {
		return err
	}

	return srv.Serve(ctx, *listen, !*noOpen)
}

func cmdInstallPath(args []string) error {
	fs := flag.NewFlagSet("install-path", flag.ContinueOnError)
	dir := fs.String("dir", "", "target directory (default: the first user-writable directory on PATH)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env := plugin.EnvFromProcess()
	link, err := plugin.InstallPath(env, *dir)
	if err != nil {
		if errors.Is(err, plugin.ErrNoPathDir) {
			fmt.Println("no directory on your PATH is writable by you; candidates would be ~/.local/bin or ~/bin once added to PATH,")
			fmt.Println("or add the product's own directory:  export PATH=\"$HOME/.statefs-ai/bin:$PATH\"")
		}

		return err
	}

	fmt.Printf("linked %s -> %s\n", link, env.Self)
	if p, ok := plugin.OnPath(); !ok || p != link {
		fmt.Println("note: open a new shell (or rehash) for the command to resolve")
	}

	return nil
}

func cmdDescribe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	title := fs.String("title", "", "short title")
	description := fs.String("description", "", "one line")
	tags := fs.String("tags", "", "comma-separated")
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}

		positional = append(positional, a)
	}

	if err := fs.Parse(args[len(positional):]); err != nil {
		return err
	}

	target := ""
	if len(positional) > 0 {
		target = positional[0]
	}

	var tg []string
	if *tags != "" {
		tg = strings.Split(*tags, ",")
	}

	return plugin.Describe(ctx, plugin.EnvFromProcess(), target, *title, *description, tg, os.Stdout)
}

func cmdDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	identity := fs.String("identity", "", "identity file that owns the conversation, or a name under ~/.statefs/identities (default: the configured identity)")
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}

		positional = append(positional, a)
	}

	if err := fs.Parse(args[len(positional):]); err != nil {
		return err
	}

	if len(positional) != 1 {
		return errors.New("delete needs one conversation name or id")
	}

	env := plugin.EnvFromProcess()
	if *identity != "" {
		env.IdentityPath = plugin.IdentityArg(*identity)
	}

	return plugin.DeleteConversation(ctx, env, positional[0], os.Stdout)
}

// cmdConfig shows where parley is enrolled, and where each value comes
// from; --directory and --statefs-ai set them in the per-user config
// ("" clears the override).
// The environment (STATEFS_DIRECTORY, STATEFS_AI_APP) still wins. --gate
// adds a delivery gate and --no-gates clears them: none is the default.
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	dir := fs.String("directory", "", "directory URL (\"\" clears the override)")
	ai := fs.String("statefs-ai", "", "statefs.ai API URL (\"\" resets to the default)")
	var gates stringList
	fs.Var(&gates, "gate", "add a delivery gate: NAME=COMMAND (the post as JSON on stdin, {\"verdict\":...} on stdout); repeatable")
	noGates := fs.Bool("no-gates", false, "remove every delivery gate")
	gateTimeout := fs.Int("gate-timeout-ms", 0, "how long one gate may take for one post (default 10000); applies to every gate")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := plugin.LoadConfig()
	set := false
	fs.Visit(func(f *flag.Flag) {
		set = true
		switch f.Name {
		case "directory":
			cfg.Directory = strings.TrimRight(*dir, "/")
		case "statefs-ai":
			cfg.StatefsAI = strings.TrimRight(*ai, "/")
		}
	})

	// Clearing comes first whatever the order on the command line, so
	// `--no-gates --gate x=y` means what it looks like: replace them.
	if *noGates {
		cfg.Gates = nil
	}

	if *gateTimeout > 0 {
		for i := range cfg.Gates {
			cfg.Gates[i].TimeoutMs = *gateTimeout
		}
	}

	for _, g := range gates {
		name, command, ok := strings.Cut(g, "=")
		name, command = strings.TrimSpace(name), strings.TrimSpace(command)
		if !ok || name == "" || command == "" {
			return errors.New("a gate is NAME=COMMAND, for example: --gate 'intent=claude -p --model haiku \"...\"'")
		}

		cfg.Gates = append(cfg.Gates, plugin.Gate{Name: name, Command: command, TimeoutMs: *gateTimeout})
	}

	if set {
		if err := plugin.SaveConfig(cfg); err != nil {
			return err
		}
	}

	env := plugin.EnvFromProcess()
	fmt.Printf("config:     %s\n", plugin.ConfigPath())
	fmt.Printf("directory:  %s  (%s)\n", env.Directory, source("STATEFS_DIRECTORY", cfg.Directory))
	fmt.Printf("statefs.ai: %s  (%s)\n", env.StatefsAI, source("STATEFS_AI_APP", cfg.StatefsAI))
	switch len(cfg.Gates) {
	case 0:
		fmt.Println("gates:      none (every post is judged by the free rules only)")
	default:
		for _, g := range cfg.Gates {
			fmt.Printf("gate:       %s: %s\n", g.Name, g.Command)
		}
	}

	if set {
		fmt.Println("running sessions and consoles keep the old values until they restart")
	}

	return nil
}

// stringList collects a flag given more than once.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// source says where a setting came from: the environment, the config, or
// the built-in default.
func source(envVar, configured string) string {
	switch {
	case os.Getenv(envVar) != "":
		return "from " + envVar
	case configured != "":
		return "from config"
	}

	return "default"
}

// cmdStatusLine prints one line for Claude Code's settings.json statusLine:
// each followed conversation and how its recent posts were judged. Local
// files only, so it is cheap enough to be redrawn constantly.
func cmdStatusLine() error {
	return plugin.StatusLine(plugin.EnvFromProcess(), os.Stdout)
}

func cmdStatus(ctx context.Context) error {
	env := plugin.EnvFromProcess()
	fmt.Printf("config:     %s\n", plugin.ConfigPath())
	fmt.Printf("directory:  %s\n", orNone(env.Directory))
	fmt.Printf("statefs.ai: %s\n", env.StatefsAI)
	fmt.Printf("identity:   %s\n", env.IdentityPath)
	if st, err := enroll.Verify(ctx, env.Directory, env.IdentityPath, env.Tenant); err == nil {
		fmt.Printf("enrolled:   %s (token exchange ok)\n", st.Username)
	} else {
		fmt.Printf("enrolled:   no (%v)\n", err)
	}

	fmt.Printf("data dir:   %s\n", env.DataDir)
	fmt.Printf("thinking:   %v\n", env.Thinking)
	live := map[string]bool{}
	if locks, err := filepath.Glob(filepath.Join(env.DataDir, "daemon-*.lock")); err == nil {
		for _, lf := range locks {
			sid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(lf), "daemon-"), ".lock")
			if plugin.DaemonRunning(env, sid) {
				live[plugin.SessionTag(sid)] = true
			}
		}
	}

	if waits := plugin.WaitReports(env); len(waits) > 0 {
		fmt.Println("background waits (parley wait), newest first:")
		shown := 0
		for _, w := range waits {
			if shown >= 5 {
				break
			}

			state := "not running"
			if w.Running {
				state = "running"
			}

			last := "never reached the directory"
			if w.State.LastOkMs > 0 {
				last = "last ok " + time.Since(time.UnixMilli(w.State.LastOkMs)).Round(time.Second).String() + " ago"
			}

			if w.Poller {
				fmt.Printf("  identity %s  %s, %s", w.Session, state, last)
				if len(w.Attached) > 0 {
					fmt.Printf(", sessions %s", strings.Join(w.Attached, ", "))
				}

				fmt.Println()
				shown++
				continue
			}

			fmt.Printf("  session %s  %s, %s", plugin.SessionTag(w.Session), state, last)
			for name, pos := range w.State.Positions {
				fmt.Printf(", %s at %d", name, pos)
			}

			for name, why := range w.State.Unreadable {
				fmt.Printf("\n    cannot read %s: %s", name, why)
			}

			if w.State.LastError != "" && len(w.State.Unreadable) == 0 {
				fmt.Printf("\n    last error: %s", w.State.LastError)
			}

			fmt.Println()
			shown++
		}
	}

	all := plugin.NamesByTime(env)
	fmt.Printf("conversations recorded from this machine: %d (most recently active first, last 10; `parley find agent=<me> --heads` for all)\n", len(all))
	for i, n := range all {
		if i >= 10 {
			break
		}

		mark := ""
		if tag := n.Name[strings.LastIndex(n.Name, "#")+1:]; live[tag] {
			mark = "  <- recording now"
		}

		fmt.Printf("  %s  %-58s %s%s\n", n.At.Local().Format("Jan 02 15:04"), n.Name, n.ID, mark)
	}

	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}

	return s
}

func cmdFakeDir(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fakedir", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8477", "address to serve on")
	username := fs.String("username", "laptop-agent", "identity to mint an enrollment URL for")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir := enroll.NewFakeDirectory()
	dir.Server.Close() // re-bind on the requested address
	srv := &http.Server{Addr: *listen, Handler: dir.Server.Config.Handler, ReadHeaderTimeout: 5 * time.Second}
	dir.Server.URL = "http://" + *listen
	go func() { <-ctx.Done(); _ = srv.Close() }()
	fmt.Printf("fake directory on http://%s\nenroll url: %s\n", *listen, enroll.Request{Directory: "http://" + *listen, Token: dir.MintToken(*username, time.Hour)}.String())
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

// splitCaps turns the --caps flag into a list; empty means "what the token grants".
func splitCaps(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}

	return strings.Split(s, ",")
}

// postBody resolves the post text: --text, or --text-file (- is stdin).
// A file spares the caller shell quoting, which a Bash tool refuses for
// multi-line markdown (a newline before a # can hide arguments).
func postBody(text, file string) (string, error) {
	if file == "" {
		if text == "" {
			return "", errors.New("post needs --text or --text-file (- for stdin)")
		}

		return text, nil
	}

	if text != "" {
		return "", errors.New("post takes --text or --text-file, not both")
	}

	var (
		b   []byte
		err error
	)
	if file == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(file)
	}

	if err != nil {
		return "", fmt.Errorf("post: read body: %w", err)
	}

	if len(b) == 0 {
		return "", errors.New("post: the body is empty")
	}

	return string(b), nil
}

// cmdMCP serves the conversation tools over stdio for an MCP client.
// Claude Code launches this from the plugin manifest; nothing is printed
// to stdout except protocol messages, so logs go to stderr.
func cmdMCP(ctx context.Context) error {
	env := plugin.EnvFromProcess()
	s := &mcp.Server{Name: "parley", Version: strings.TrimSpace(version), Tools: mcp.Tools(env)}
	return s.Serve(ctx, os.Stdin, os.Stdout)
}

// cmdLabels lists the scope labels in use, the vocabulary a search filters on.
func cmdLabels(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("labels", flag.ContinueOnError)
	limit := fs.Int("limit", 500, "how many conversations to aggregate over")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return plugin.Labels(ctx, plugin.EnvFromProcess(), *limit, os.Stdout)
}

func anyVersion(v string) string {
	if v == "" {
		return "any version"
	}

	return v
}

// cmdVersion prints the build version and, from the directory, the statefs
// release and the version floors both ways; --check asks GitHub for the
// newest release and says whether this one is behind. Only "parley X" goes
// to stdout: the plugin wrapper reads the installed version with
// `parley version 2>/dev/null | awk '{print $2}'`, and a second stdout line
// made it rebuild on every call (and older plugin roots downgrade the binary).
func cmdVersion(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	check := fs.Bool("check", false, "ask for the newest release and compare")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env := plugin.EnvFromProcess()
	cur := strings.TrimSpace(version)
	fmt.Printf("parley %s\n", cur)
	if s, ok := plugin.CheckServer(ctx, env, *check); ok {
		switch {
		case s.Missing:
			fmt.Fprintln(os.Stderr, "statefs: version not published (a server older than the version route)")
		default:
			fmt.Fprintf(os.Stderr, "statefs %s (parley needs %s or newer; this statefs accepts parley %s or newer)\n",
				strings.TrimPrefix(s.Version, "v"), plugin.MinServer, anyVersion(s.MinClients[plugin.ClientName]))
		}
		for _, n := range plugin.FloorNotices(s, cur) {
			fmt.Fprintln(os.Stderr, n)
		}
	}

	if !*check {
		if n := plugin.UpdateNotice(env, cur); n != "" {
			fmt.Fprintln(os.Stderr, n)
		}

		return nil
	}

	latest, err := plugin.CheckLatest(ctx, env, true)
	if err != nil {
		return err
	}

	switch {
	case latest == "":
		fmt.Fprintln(os.Stderr, "could not reach the release list (needs `gh` and access to the repo)")
	case plugin.NewerVersion(cur, latest):
		fmt.Fprintf(os.Stderr, "update available: %s\n", latest)
	default:
		fmt.Fprintln(os.Stderr, "up to date")
	}

	return nil
}
