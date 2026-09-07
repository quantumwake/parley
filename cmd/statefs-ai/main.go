// statefs-ai is the plugin binary: Claude Code hooks call `statefs-ai hook`,
// people call `statefs-ai enroll <url>` and `statefs-ai whoami`, and
// `statefs-ai fakedir` runs the test directory for local spikes.
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
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
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
	case "find":
		err = cmdFind(ctx, os.Args[2:])
	case "daemon":
		err = cmdDaemon(ctx, os.Args[2:])
	case "replay":
		err = cmdReplay(ctx, os.Args[2:])
	case "cleanup-conformance":
		err = cmdCleanup(ctx, os.Args[2:])
	case "fakedir":
		err = cmdFakeDir(ctx, os.Args[2:])
	case "version":
		fmt.Println("statefs-ai " + strings.TrimSpace(version))
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "statefs-ai:", err)
		switch {
		case errors.Is(err, enroll.ErrTokenRejected), errors.Is(err, enroll.ErrAlreadyEnrolled):
			os.Exit(3)
		}

		os.Exit(4)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  statefs-ai enroll <url|token> [--directory URL] [--label L] [--out PATH] [--reset]
  statefs-ai whoami [--directory URL] [--identity PATH] [--tenant T]
  statefs-ai status          (enrollment, directory, last conversations)
  statefs-ai find [k=v ...] [--limit N]   (conversations on the server by scope, e.g. session=<id> agent=<name>)
  statefs-ai hook            (reads Claude Code hook JSON on stdin)
  statefs-ai daemon --session ID --transcript PATH [--cwd DIR]
  statefs-ai replay <namespace-id|display-name> [--from N] [--json] [--diff TRANSCRIPT]
  statefs-ai cleanup-conformance [--directory URL] [--dry-run]   (needs a manage-capable credential)
  statefs-ai fakedir [--listen :8477] [--username U]
  statefs-ai version`)
}

func cmdEnroll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	directory := fs.String("directory", os.Getenv("STATEFS_DIRECTORY"), "directory base URL when the token is bare")
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
	directory := fs.String("directory", os.Getenv("STATEFS_DIRECTORY"), "directory base URL")
	path := fs.String("identity", os.Getenv("STATEFS_KEY_FILE"), "identity file path")
	tenant := fs.String("tenant", os.Getenv("STATEFS_TENANT"), "acting tenant")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *directory == "" {
		return errors.New("--directory or STATEFS_DIRECTORY is required")
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
	directory := fs.String("directory", os.Getenv("STATEFS_DIRECTORY"), "directory base URL")
	dry := fs.Bool("dry-run", false, "list only")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *directory == "" {
		return errors.New("--directory or STATEFS_DIRECTORY is required")
	}

	return plugin.CleanupConformance(ctx, strings.TrimRight(*directory, "/"), *dry, os.Stdout)
}

func cmdFind(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	limit := fs.Int("limit", 100, "max results")
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

	return plugin.Find(ctx, plugin.EnvFromProcess(), pairs, *limit, os.Stdout)
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
