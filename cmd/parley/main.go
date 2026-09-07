// parley is the plugin binary: Claude Code hooks call `parley hook`,
// people call `parley enroll <url>` and `parley whoami`, and
// `parley fakedir` runs the test directory for local spikes.
package main

import (
	_ "embed"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/enroll"
	"github.com/quantumwake/statefs.ai/pkg/plugin"
)

//go:embed VERSION
var version string

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
		usage()
		os.Exit(0)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "hook":
		err = plugin.Handle(ctx, plugin.EnvFromProcess(), os.Stdin, os.Stdout)
	case "enroll":
		err = cmdEnroll(ctx, os.Args[2:])
	case "whoami":
		err = cmdWhoami(ctx, os.Args[2:])
	case "status":
		err = cmdStatus(ctx)
	case "install-path":
		err = cmdInstallPath(os.Args[2:])
	case "find":
		err = cmdFind(ctx, os.Args[2:])
	case "conversation":
		err = cmdConversation(ctx, os.Args[2:])
	case "create", "list", "join", "leave", "subscriptions", "post", "read", "grant":
		err = cmdConversation(ctx, os.Args[1:])
	case "daemon":
		err = cmdDaemon(ctx, os.Args[2:])
	case "replay":
		err = cmdReplay(ctx, os.Args[2:])
	case "cleanup-conformance":
		err = cmdCleanup(ctx, os.Args[2:])
	case "fakedir":
		err = cmdFakeDir(ctx, os.Args[2:])
	case "version":
		fmt.Println("parley " + strings.TrimSpace(version))
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
	fmt.Fprint(os.Stderr, `parley `+strings.TrimSpace(version)+`  (statefs.ai parley: your Claude Code sessions, recorded and shared on statefs.io)

Every Claude Code session on this machine is recorded as a conversation on
statefs.io once the machine is enrolled. Conversations you create for a group
are shared conversations: others in your tenant can find them, and, once
granted access, follow them and post to them.

SETUP
  parley enroll <url|token>     enroll this machine for the logged-on user with a URL
                                minted by your statefs.io tenant admin (single use)
                                  --caps read,write[,manage]  --out PATH  --reset  --label L
  parley status                 enrollment, directory, and conversations recorded here
  parley whoami                 prove the identity can log in
  parley install-path [--dir D] link parley into a directory on your PATH

YOUR RECORDED SESSIONS
  parley find [k=v ...]         conversations on statefs.io by label, one directory query
                                  e.g.  parley find agent=<me>   parley find session=<id>
                                  --heads also reads each conversation's row count (slower)
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
  parley post <name> --text T   say something   --kind question|answer|comment|report|status
                                  --to <user>  --reply-to <event id>  --tags a,b
  parley read <name>            catch up from your cursor   --from N   --peek (keep the cursor)
  parley grant <name> --user U  give a member access   --access read|write|read,write
                                (tenant admin credential required)

INTERNAL (called by the Claude Code plugin)
  parley hook                   reads a hook event on stdin
  parley daemon --session ID --transcript PATH [--cwd DIR]
  parley fakedir [--listen :8477] [--username U]     a fake directory for tests
  parley cleanup-conformance [--dry-run]             delete test namespaces (manage)
  parley version

Directory: STATEFS_DIRECTORY, else ~/.statefs-ai/config.json, else https://directory.statefs.io
Identity:  STATEFS_KEY_FILE, else the config, else ~/.statefs/identity
`)
}

func cmdEnroll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	directory := fs.String("directory", plugin.EnvFromProcess().Directory, "directory base URL when the token is bare (default: the config, then statefs.io)")
	label := fs.String("label", "", "label for the registered key (default statefs-ai@hostname)")
	out := fs.String("out", os.Getenv("STATEFS_KEY_FILE"), "identity file path (default ~/.statefs/identity)")
	reset := fs.Bool("reset", false, "replace an existing identity file")
	tenant := fs.String("tenant", os.Getenv("STATEFS_TENANT"), "acting tenant for the verification exchange")
	caps := fs.String("caps", "read,write", "capabilities to request for this key: read,write[,manage]")
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

	res, err := enroll.Enroll(ctx, req, enroll.Options{Label: *label, Path: *out, Reset: *reset, Caps: strings.Split(*caps, ",")})
	if err != nil {
		return err
	}

	fmt.Printf("enrolled: %s\nidentity file: %s\npublic key: %s\n", res.Username, res.Path, res.PublicKey)
	st, err := enroll.Verify(ctx, res.Directory, res.Path, *tenant)
	if err != nil {
		return fmt.Errorf("enrolled but the token exchange failed: %w", err)
	}

	fmt.Printf("verified: acting token issued for %s at %s\n", st.Username, st.Directory)
	if err := plugin.SaveConfig(plugin.Config{Directory: res.Directory, Identity: res.Path, Tenant: *tenant}); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	fmt.Printf("config: %s (hooks now need no environment variables)\n", plugin.ConfigPath())
	return nil
}

func cmdWhoami(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	env := plugin.EnvFromProcess()
	directory := fs.String("directory", env.Directory, "directory base URL")
	path := fs.String("identity", env.IdentityPath, "identity file path")
	tenant := fs.String("tenant", env.Tenant, "acting tenant")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := enroll.Verify(ctx, strings.TrimRight(*directory, "/"), *path, *tenant)
	if err != nil {
		return err
	}

	fmt.Printf("identity: %s\nfile: %s\ndirectory: %s\nexchange: ok\n", st.Username, st.Path, st.Directory)
	return nil
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
	kind := fs.String("kind", "comment", "question | answer | comment | report | status | artifact | request | claim")
	to := fs.String("to", "*", "identity, or * for everyone")
	replyTo := fs.String("reply-to", "", "event id this answers")
	from := fs.Int64("from", -1, "first position (default: the subscription cursor)")
	peek := fs.Bool("peek", false, "do not advance the cursor")
	user := fs.String("user", "", "member username to grant")
	access := fs.String("access", "read", "read | write | read,write")
	if err := fs.Parse(flags); err != nil {
		return err
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
		return plugin.Join(ctx, env, name, *mode, *pick, os.Stdout)
	case "leave":
		return plugin.Leave(env, name, os.Stdout)
	case "subscriptions":
		for _, s := range plugin.Subscriptions(env) {
			fmt.Printf("%-28s %-6s cursor=%d %s\n", s.Name, s.Mode, s.Cursor, s.ID)
		}

		return nil
	case "post":
		if *text == "" {
			return errors.New("post needs --text")
		}

		return plugin.Post(ctx, env, name, *kind, *text, *to, *replyTo, split(*tags), os.Stdout)
	case "read":
		return plugin.Read(ctx, env, name, *from, *peek, os.Stdout)
	case "grant":
		if *user == "" {
			return errors.New("grant needs --user")
		}

		return plugin.GrantAccess(ctx, env, name, *user, *access, os.Stdout)
	}

	return fmt.Errorf("unknown conversation subcommand %q", sub)
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

func cmdStatus(ctx context.Context) error {
	env := plugin.EnvFromProcess()
	fmt.Printf("config:     %s\n", plugin.ConfigPath())
	fmt.Printf("directory:  %s\n", orNone(env.Directory))
	fmt.Printf("identity:   %s\n", env.IdentityPath)
	if st, err := enroll.Verify(ctx, env.Directory, env.IdentityPath, env.Tenant); err == nil {
		fmt.Printf("enrolled:   %s (token exchange ok)\n", st.Username)
	} else {
		fmt.Printf("enrolled:   no (%v)\n", err)
	}

	fmt.Printf("data dir:   %s\n", env.DataDir)
	fmt.Printf("thinking:   %v\n", env.Thinking)
	names := plugin.Names(env)
	fmt.Printf("conversations captured from this data dir: %d\n", len(names))
	for n, id := range names {
		fmt.Printf("  %-45s %s\n", n, id)
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
