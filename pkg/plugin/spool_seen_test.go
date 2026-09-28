package plugin

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A comment that arrives with a question is kept for the next prompt while
// the wait wakes for the question. If the session then reads the channel
// itself, which is how an agent catches up, that comment must not be shown
// again at the next prompt: a post is displayed once, by whichever route
// reaches it first.
func TestAPostASessionReadItselfIsNotShownAgainFromTheSpool(t *testing.T) {
	for _, peek := range []bool{true, false} {
		name := "read"
		if peek {
			name = "peek"
		}

		t.Run(name, func(t *testing.T) {
			s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
			a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
			follow(t, a, "issues")
			_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
			withWaitStore(t, a)

			if err := Post(context.Background(), b, "issues", "comment", "kept-comment", "", "", nil, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}

			if err := Post(context.Background(), b, "issues", "question", "wakes-the-wait", "", "", nil, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}

			var woke bytes.Buffer
			if err := Wait(context.Background(), a, nil, 5*time.Second, &woke); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(woke.String(), "wakes-the-wait") {
				t.Fatalf("the wait should wake for the question:\n%s", woke.String())
			}

			if _, err := os.Stat(contextPath(a)); err != nil {
				t.Fatalf("the comment should be kept for the next prompt: %v", err)
			}

			var read bytes.Buffer
			if err := Read(context.Background(), a, "issues", 0, peek, 0, &read); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(read.String(), "kept-comment") {
				t.Fatalf("the read should show the comment:\n%s", read.String())
			}

			if got := Inject(context.Background(), a); strings.Contains(got, "kept-comment") {
				t.Fatalf("the prompt showed a comment the session had already read:\n%s", got)
			}
		})
	}
}

// Reading part of a conversation removes only what it printed. A comment the
// session has not been shown by any route is still there for its next prompt.
func TestReadingOnePostLeavesTheOtherKeptPostsForThePrompt(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	withWaitStore(t, a)
	ctx := context.Background()

	for _, w := range []string{"first-kept", "second-kept"} {
		if err := Post(ctx, b, "issues", "comment", w, "", "", nil, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	if err := Post(ctx, b, "issues", "question", "wakes-the-wait", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	if err := Wait(ctx, a, nil, 5*time.Second, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	// Read from just past the first comment: it prints the second comment and
	// the question, and not the first.
	var read bytes.Buffer
	if err := Read(ctx, a, "issues", 1, true, 0, &read); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(read.String(), "first-kept") || !strings.Contains(read.String(), "second-kept") {
		t.Fatalf("the read should start after the first comment:\n%s", read.String())
	}

	got := Inject(ctx, a)
	if !strings.Contains(got, "first-kept") {
		t.Fatalf("a comment no route had shown was dropped from the prompt:\n%s", got)
	}

	if strings.Contains(got, "second-kept") {
		t.Fatalf("a comment the read printed was shown again:\n%s", got)
	}
}

// Nothing to do outside a session, with an empty list, or with no spool: it
// must not create a file or fail.
func TestUnspoolIsANoOpWhereThereIsNothingToForget(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]

	unspool(a, []string{"01M3FM20513XTTNXB2QFRTHN7Q"}) // no spool yet
	if _, err := os.Stat(contextPath(a)); err == nil {
		t.Fatal("unspool created a spool where there was none")
	}

	spoolContext(a, []string{"- [issues] post.comment x (01AAAAAAAAAAAAAAAAAAAAAAAA) @1: kept"})
	unspool(a, nil)
	unspool(a, []string{""})

	if got := drainContext(a); len(got) != 1 {
		t.Fatalf("an empty id list changed the spool: %v", got)
	}

	terminal := a
	terminal.Session = ""
	unspool(terminal, []string{"01M3FM20513XTTNXB2QFRTHN7Q"}) // no session, nothing to keep
}

// Only the exact " (<id>) @<n>" a rendered post carries counts. A comment
// whose own text happens to contain another post's id is not dropped.
func TestUnspoolMatchesAPostsIdNotItsText(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]

	const shownID = "01BBBBBBBBBBBBBBBBBBBBBBBB"

	spoolContext(a, []string{
		"- [issues] post.comment x (01CCCCCCCCCCCCCCCCCCCCCCCC) @1: see " + shownID + " above",
		"- [issues] post.comment x (" + shownID + ") @2: the shown one",
	})

	unspool(a, []string{shownID})

	got := drainContext(a)
	if len(got) != 1 || !strings.Contains(got[0], "see "+shownID+" above") {
		t.Fatalf("only the shown post should go, and a mention of its id should stay: %v", got)
	}
}

// Two takers must never share a taken file: a rename replaces an existing
// target, so a take made while another is still unread would overwrite its
// lines (lumen's review of #124).
func TestATakeStillUnreadSurvivesASecondTake(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	path := contextPath(a)

	// A prompt hook takes the file and has not read it yet.
	spoolContext(a, []string{"- [issues] post.comment x (01AAAAAAAAAAAAAAAAAAAAAAAA) @1: first"})
	first, ok := takeFile(path)
	if !ok {
		t.Fatal("the first take found nothing")
	}

	// A wait keeps another post, and a read unspools something else: a
	// second take, of the new file.
	spoolContext(a, []string{"- [issues] post.comment x (01BBBBBBBBBBBBBBBBBBBBBBBB) @2: second"})
	unspool(a, []string{"01ZZZZZZZZZZZZZZZZZZZZZZZZ"})

	got := readTaken(first)
	if len(got) != 1 || !strings.Contains(got[0], "first") {
		t.Fatalf("the first take's lines were replaced by the second's: %v", got)
	}

	if rest := drainContext(a); len(rest) != 1 || !strings.Contains(rest[0], "second") {
		t.Fatalf("the second post stays kept for the prompt: %v", rest)
	}
}

func TestUnspoolKeepsACommentQuotingAShownPostsHeader(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]

	const shownID = "01BBBBBBBBBBBBBBBBBBBBBBBB"

	spoolContext(a, []string{
		// Agents paste hook output into their posts, headers included.
		"- [issues] post.comment y (01CCCCCCCCCCCCCCCCCCCCCCCC) @3:\n    as you said at reply-to:z (" + shownID + ") @2: the shown one",
		"- [issues] post.comment x (" + shownID + ") @2: the shown one",
		// A work note carries a "(…)" and an "@n" of its own; neither is the post's id.
		"- [issues] [work: claimed by w (01DDDDDDDDDDDDDDDDDDDDDDDD) at @4] post.claim w (01EEEEEEEEEEEEEEEEEEEEEEEE) @5: mine",
	})

	unspool(a, []string{shownID, "01DDDDDDDDDDDDDDDDDDDDDDDD"})

	got := drainContext(a)
	if len(got) != 2 || !strings.Contains(got[0], "as you said") || !strings.Contains(got[1], "mine") {
		t.Fatalf("only the shown post goes; a quote of its header and a work note's id do not count: %v", got)
	}
}

func TestHeaderIDIsThePostsOwn(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{
			line: "- [c] post.comment x (01AAAAAAAAAAAAAAAAAAAAAAAA) @1: hi",
			want: "01AAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{
			line: "- [c] post.comment x (host#1a2b) to:y reply-to:r (01AAAAAAAAAAAAAAAAAAAAAAAA) @7:\n    text",
			want: "01AAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{
			line: "- [c] [work: claimed by w (host#1a2b) at @4] post.claim w (01EEEEEEEEEEEEEEEEEEEEEEEE) @5: mine",
			want: "01EEEEEEEEEEEEEEEEEEEEEEEE",
		},
		{
			line: "- [c] post.comment x (01AAAAAAAAAAAAAAAAAAAAAAAA) @1: quoting y (01BBBBBBBBBBBBBBBBBBBBBBBB) @2: b",
			want: "01AAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{line: "not a post at all", want: ""},
	} {
		if got := headerID(c.line); got != c.want {
			t.Errorf("headerID(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}
