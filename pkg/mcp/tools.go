package mcp

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/quantumwake/parley/pkg/plugin"
)

// Tools returns the conversation operations an agent can call directly.
// Each one wraps the same function the CLI calls, so behaviour cannot drift
// between the two surfaces.
func Tools(env plugin.Env) []Tool {
	return []Tool{
		{
			Name: "search_conversations",
			Description: "Find shared conversations in this tenant that I may read or write: the ones open to everyone plus any I have been granted. " +
				"Use it to discover what exists before joining. Optional free-text and tag filters.",
			Schema: obj(nil, map[string]any{
				"query": prop("string", "match against name and description"),
				"tag":   prop("string", "only conversations carrying this tag"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				return plugin.ListShared(ctx, env, a.Str("tag"), a.Str("query"), w)
			},
		},
		{
			Name: "list_labels",
			Description: "List the scope labels that exist across the conversations I can see, with their values. " +
				"Call this before searching: it is the vocabulary to filter on, so a search narrows on labels that actually exist rather than guesses.",
			Schema: obj(nil, map[string]any{
				"limit": prop("integer", "how many conversations to aggregate over (default 500)"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				limit, _ := a.Int("limit")
				return plugin.Labels(ctx, env, int(limit), w)
			},
		},
		{
			Name: "create_conversation",
			Description: "Create a shared conversation that others can join. I own it, so I can grant access to it and delete it. " +
				"Give it a description: that is what others see when searching.",
			Schema: obj([]string{"name"}, map[string]any{
				"name":        prop("string", "unique name in the tenant, e.g. platform-notes"),
				"description": prop("string", "one line saying what belongs here"),
				"tags":        listProp("free labels for search"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				if a.Str("name") == "" {
					return errors.New("name is required")
				}

				return plugin.CreateShared(ctx, env, a.Str("name"), a.Str("description"), a.Strings("tags"), w)
			},
		},
		{
			Name: "join_conversation",
			Description: "Subscribe to a shared conversation. New posts are then shown to me when the user sends a prompt and when my turn ends, but never while I am idle: " +
				"to be woken, run `parley wait` as a background shell task and run it again after handling what it prints. " +
				"Set `as` to the handle I should speak under in this session, which is how others tell me apart from other sessions using the same identity.",
			Schema: obj([]string{"name"}, map[string]any{
				"name": prop("string", "the conversation to follow"),
				"as":   prop("string", "handle to speak under here, e.g. reviewer"),
				"mode": enumProp("full delivers every post; digest delivers only reports, statuses and summaries", "full", "digest"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				if a.Str("name") == "" {
					return errors.New("name is required")
				}

				return plugin.Join(ctx, env, a.Str("name"), a.Str("mode"), "all", a.Str("as"), w)
			},
		},
		{
			Name:        "leave_conversation",
			Description: "Unsubscribe from a shared conversation. It keeps existing; I simply stop receiving its posts.",
			Schema: obj([]string{"name"}, map[string]any{
				"name": prop("string", "the conversation to stop following"),
			}),
			Call: func(_ context.Context, a Args, w io.Writer) error {
				if a.Str("name") == "" {
					return errors.New("name is required")
				}

				return plugin.Leave(env, a.Str("name"), w)
			},
		},
		{
			Name: "post_message",
			Description: "Post one message to a shared conversation. Text may be as long and as multi-line as needed, including markdown headings and code blocks: " +
				"there is no shell quoting here. Use `to` to address someone by identity or handle, and `reply_to` to answer a specific message. " +
				plugin.WorkGuide("parley") + " (the list_work tool does the same).",
			Schema: obj([]string{"name", "text"}, map[string]any{
				"name":     prop("string", "the conversation to post to"),
				"text":     prop("string", "the message body; markdown is fine"),
				"kind":     enumProp("exchange: comment, question, answer, report, status, artifact; work: request, claim, close", "comment", "question", "answer", "report", "status", "artifact", "request", "claim", "close"),
				"outcome":  enumProp("for kind close: how the work ended", "resolved", "handed_over", "dropped"),
				"to":       prop("string", "an identity or handle to address, or * for everyone"),
				"reply_to": prop("string", "the event id this answers, from a message I read"),
				"tags":     listProp("free labels"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				name, text := a.Str("name"), a.Str("text")
				if name == "" || text == "" {
					return errors.New("name and text are required")
				}

				kind := a.Str("kind")
				if kind == "" {
					kind = "comment"
				}

				to := a.Str("to")
				if to == "" {
					to = "*"
				}

				return plugin.Post(ctx, env, name, kind, text, to, a.Str("reply_to"), a.Strings("tags"), w, plugin.WithOutcome(a.Str("outcome")))
			},
		},
		{
			Name: "list_work",
			Description: "List the work in the conversations I follow: requests nobody has claimed, work claimed and by whom (mine marked), and optionally closed work. " +
				"Check it before starting anything beyond a quick read, so I claim open work instead of duplicating someone's.",
			Schema: obj(nil, map[string]any{
				"name": prop("string", "only this conversation; omit for all I follow"),
				"all":  prop("boolean", "include closed work"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				var names []string
				if n := a.Str("name"); n != "" {
					names = []string{n}
				}

				all, _ := a["all"].(bool)
				return plugin.ListWork(ctx, env, names, all, w)
			},
		},
		{
			Name: "read_conversation",
			Description: "Read a shared conversation from my cursor, or from a position. " +
				"With wait_seconds it blocks until someone else posts (my own posts do not end the wait), so I can wait for another agent inside my own turn. " +
				"To wait while idle instead, run `parley wait` as a background shell task.",
			Schema: obj([]string{"name"}, map[string]any{
				"name":         prop("string", "the conversation to read"),
				"from":         prop("integer", "first position to read; omit to continue from my cursor, 0 for the beginning"),
				"peek":         prop("boolean", "read without advancing my cursor"),
				"wait_seconds": prop("integer", "block up to this long for at least one new message"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				if a.Str("name") == "" {
					return errors.New("name is required")
				}

				from := int64(-1) // -1 means "from my cursor"
				if v, ok := a.Int("from"); ok {
					from = v
				}

				var wait time.Duration
				if v, ok := a.Int("wait_seconds"); ok && v > 0 {
					wait = time.Duration(v) * time.Second
				}

				return plugin.Read(ctx, env, a.Str("name"), from, a.Bool("peek"), wait, w)
			},
		},
		{
			Name:        "my_subscriptions",
			Description: "List the shared conversations I follow, with how much is unread in each and the handle I speak under.",
			Schema:      obj(nil, map[string]any{}),
			Call: func(ctx context.Context, _ Args, w io.Writer) error {
				return plugin.ShowSubscriptions(ctx, env, w)
			},
		},
		{
			Name:        "grant_access",
			Description: "Give another identity access to a conversation I own. Read lets them follow it; write lets them post.",
			Schema: obj([]string{"name", "user"}, map[string]any{
				"name":   prop("string", "the conversation I own"),
				"user":   prop("string", "the identity username to grant"),
				"access": enumProp("what to grant", "read", "write", "read,write"),
			}),
			Call: func(ctx context.Context, a Args, w io.Writer) error {
				if a.Str("name") == "" || a.Str("user") == "" {
					return errors.New("name and user are required")
				}

				access := a.Str("access")
				if access == "" {
					access = "read"
				}

				return plugin.GrantAccess(ctx, env, a.Str("name"), a.Str("user"), access, w)
			},
		},
		{
			Name:        "whoami",
			Description: "Show which statefs identity this machine acts as, and what that credential is allowed to do.",
			Schema:      obj(nil, map[string]any{}),
			Call: func(ctx context.Context, _ Args, w io.Writer) error {
				return plugin.WhoAmI(ctx, env, w)
			},
		},
	}
}
