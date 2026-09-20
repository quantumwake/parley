package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	sfs "github.com/quantumwake/statefs/client"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// Options tune one enrollment.
type Options struct {
	Label string       // per-machine label on the registered key (default: hostname)
	Caps  []string     // capability subset for this key (default: everything the enrollment token grants)
	Path  string       // identity file path (default: identityfile.DefaultPath())
	Reset bool         // overwrite an existing identity file
	HTTP  *http.Client // nil = 30 s default
	Now   func() time.Time
}

// Result is what a successful enrollment produced.
type Result struct {
	Username  string
	PublicKey string
	Path      string
	Directory string
}

// Errors callers branch on.
var (
	ErrAlreadyEnrolled = errors.New("enroll: identity file exists (pass Reset to replace it)")
	ErrTokenRejected   = errors.New("enroll: token invalid, expired, or already used")
)

// Enroll performs the whole flow: keygen locally, register the public key
// with the token, write the identity file. Nothing private leaves the
// machine. The token is consumed on success and on some failures (the
// directory burns it before registering), so a retry needs a new token.
func Enroll(ctx context.Context, req Request, o Options) (Result, error) {
	if req.Directory == "" || req.Token == "" {
		return Result{}, fmt.Errorf("%w: directory and token are required", ErrBadURL)
	}

	if o.Path == "" {
		o.Path = identityfile.DefaultPath()
	}

	if _, err := os.Stat(o.Path); err == nil && !o.Reset {
		return Result{}, ErrAlreadyEnrolled
	}

	if o.Label == "" {
		h, _ := os.Hostname()
		o.Label = "parley@" + h
	}

	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}

	// Username is learned from the directory's answer; generate with a
	// placeholder and fill it in after.
	f, err := identityfile.Generate("pending")
	if err != nil {
		return Result{}, err
	}

	body, _ := json.Marshal(map[string]any{
		"token": req.Token, "public_key": f.PublicKey, "alg": f.Alg, "label": o.Label, "caps": o.Caps,
	})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Directory+"/auth/enroll", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}

	hreq.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(hreq)
	if err != nil {
		return Result{}, err
	}

	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return Result{}, fmt.Errorf("%w: %s", ErrTokenRejected, strings.TrimSpace(string(msg)))
	default:
		return Result{}, fmt.Errorf("enroll: %s answered HTTP %d: %s", req.Directory, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Authenticator struct {
			Username string `json:"username"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal(msg, &out); err != nil || out.Authenticator.Username == "" {
		return Result{}, fmt.Errorf("enroll: unexpected answer: %s", strings.TrimSpace(string(msg)))
	}

	f.Username = out.Authenticator.Username
	if err := identityfile.Write(o.Path, f); err != nil {
		return Result{}, err
	}

	return Result{Username: f.Username, PublicKey: f.PublicKey, Path: o.Path, Directory: req.Directory}, nil
}

// Status is what Verify learned about the local identity.
type Status struct {
	Username  string
	Path      string
	Directory string
	Tenant    string
}

// Verify proves the local identity works: it exchanges a signed assertion
// for an acting token through the SDK and discards the token.
func Verify(ctx context.Context, directory, path, tenant string) (Status, error) {
	if path == "" {
		path = identityfile.DefaultPath()
	}

	f, err := identityfile.Read(path)
	if err != nil {
		return Status{}, err
	}

	c := sfs.New(directory, "", nil)
	c.Credentials = sfs.Credentials{Identity: &f, Tenant: tenant}
	if _, err := c.ActingToken(ctx); err != nil {
		return Status{}, err
	}

	return Status{Username: f.Username, Path: path, Directory: directory, Tenant: tenant}, nil
}
