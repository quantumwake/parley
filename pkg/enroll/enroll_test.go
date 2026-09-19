package enroll

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/quantumwake/statefs/pkg/identityfile"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in       string
		dir, tok string
		wantErr  bool
	}{
		{"https://directory.statefs.io/enroll?token=en_abc", "https://directory.statefs.io", "en_abc", false},
		{"https://directory.statefs.io/enroll#en_abc", "https://directory.statefs.io", "en_abc", false},
		{"statefs://enroll?directory=https://d.example&token=en_x", "https://d.example", "en_x", false},
		{"en_bare", "", "en_bare", false},
		{"https://directory.statefs.io/other?token=en_abc", "", "", true},
		{"not a url at all", "", "", true},
	}
	for _, c := range cases {
		r, err := ParseURL(c.in)
		if c.wantErr {
			if !errors.Is(err, ErrBadURL) {
				t.Fatalf("%q: want ErrBadURL, got %v", c.in, err)
			}

			continue
		}

		if err != nil || r.Directory != c.dir || r.Token != c.tok {
			t.Fatalf("%q: got %+v %v", c.in, r, err)
		}
	}
}

// TestEnrollThenExchange is the spike's oracle: an admin-minted URL turns
// into a working identity, the token cannot be reused, and a reset needs
// a fresh token.
func TestEnrollThenExchange(t *testing.T) {
	ctx := context.Background()
	dir := NewFakeDirectory()
	defer dir.Close()
	path := filepath.Join(t.TempDir(), "identity")

	req, err := ParseURL(dir.EnrollURL("reviewer-bot"))
	if err != nil {
		t.Fatal(err)
	}

	res, err := Enroll(ctx, req, Options{Path: path, Label: "test"})
	if err != nil {
		t.Fatal(err)
	}

	if res.Username != "reviewer-bot" || dir.Enrolled != 1 {
		t.Fatalf("enroll: %+v enrolled=%d", res, dir.Enrolled)
	}

	f, err := identityfile.Read(path)
	if err != nil || f.Username != "reviewer-bot" || f.PublicKey != res.PublicKey {
		t.Fatalf("identity file: %+v %v", f, err)
	}

	st, err := Verify(ctx, dir.URL(), path, "")
	if err != nil || st.Username != "reviewer-bot" || dir.Exchanges != 1 {
		t.Fatalf("verify: %+v %v exchanges=%d", st, err, dir.Exchanges)
	}

	// The same token again is refused; an existing file is protected.
	if _, err := Enroll(ctx, req, Options{Path: path, Reset: true}); !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("reuse: want ErrTokenRejected, got %v", err)
	}

	if _, err := Enroll(ctx, req, Options{Path: path}); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("existing file: want ErrAlreadyEnrolled, got %v", err)
	}

	// A fresh token with Reset rotates the key; the old key still verifies
	// (the fake keeps every registered key, like the directory does).
	req2, _ := ParseURL(dir.EnrollURL("reviewer-bot"))
	res2, err := Enroll(ctx, req2, Options{Path: path, Reset: true})
	if err != nil || res2.PublicKey == res.PublicKey {
		t.Fatalf("reset: %+v %v", res2, err)
	}

	if _, err := Verify(ctx, dir.URL(), path, ""); err != nil {
		t.Fatalf("verify after reset: %v", err)
	}
}

func TestEnrollWrongKeyDoesNotExchange(t *testing.T) {
	ctx := context.Background()
	dir := NewFakeDirectory()
	defer dir.Close()
	path := filepath.Join(t.TempDir(), "identity")
	f, _ := identityfile.Generate("nobody")
	_ = identityfile.Write(path, f)
	if _, err := Verify(ctx, dir.URL(), path, ""); err == nil {
		t.Fatal("an unregistered key must not exchange")
	}
}

// A portal may append fields after the token in the fragment (it never
// reaches a server log); the token stays exactly the token.
func TestParseURLFragmentFields(t *testing.T) {
	r, err := ParseURL("https://directory.example/enroll#en_abc123&statefs_ai=https%3A%2F%2Fapp.dev.example%2F&tenant=acme")
	if err != nil {
		t.Fatal(err)
	}
	if r.Token != "en_abc123" || r.StatefsAI != "https://app.dev.example" || r.Tenant != "acme" || r.Directory != "https://directory.example" {
		t.Fatalf("%+v", r)
	}

	if r, _ := ParseURL("https://directory.example/enroll#en_abc123&statefs_ai=https://app.dev.example"); r.Token != "en_abc123" || r.StatefsAI != "https://app.dev.example" {
		t.Fatalf("unescaped value: %+v", r)
	}

	if r, _ := ParseURL("https://directory.example/enroll#en_abc123"); r.Token != "en_abc123" || r.StatefsAI != "" {
		t.Fatalf("plain fragment unchanged: %+v", r)
	}
}
