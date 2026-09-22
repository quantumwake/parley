// Package agentaccess signs a parley identity in to statefs.ai and looks
// people up there, so a conversation can be granted to a colleague's agent
// without knowing its exact username (statefs.ai docs/design/08).
//
// Sign-in: the identity signs {sub, aud: "statefs.ai/agent", iat, nonce}
// with its own ed25519 key in the statefs assertion format and trades it at
// POST /api/v1/agent/token for a 15-minute bearer token. statefs.ai asks
// statefs whether the key is live; nothing here holds a statefs.ai secret.
package agentaccess

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Audience is what statefs.ai accepts; statefs refuses it at its own token
// exchange, so an assertion made here cannot sign in there.
const Audience = "statefs.ai/agent"

// DefaultBase is statefs.ai's API; STATEFS_AI_APP overrides it.
const DefaultBase = "https://app.statefs.ai"

var (
	// ErrSignIn: statefs.ai refused the assertion. Until statefs ships its
	// verify route this is every identity; after, it is one statefs.ai
	// did not issue, or a revoked key.
	ErrSignIn = errors.New("statefs.ai refused this identity's sign-in")
	// ErrLookupOff: the organization's owner turned people lookup off.
	ErrLookupOff = errors.New("people lookup is turned off for this organization")
	// ErrUnavailable: statefs.ai (or statefs behind it) could not answer.
	ErrUnavailable = errors.New("statefs.ai is unavailable")
)

// Agent is one of a person's agents; Identity is what a grant names.
type Agent struct {
	Label    string `json:"label"`
	Identity string `json:"identity"`
}

// Person is a confirmed member of the caller's organization.
type Person struct {
	Name   string  `json:"name"`
	Agents []Agent `json:"agents"`
}

// Base is statefs.ai's API root for this process.
func Base() string {
	if b := strings.TrimRight(os.Getenv("STATEFS_AI_APP"), "/"); b != "" {
		return b
	}

	return DefaultBase
}

// Sign makes an assertion for username in the statefs wire format:
// base64url(json).base64url(ed25519 signature over that json).
func Sign(priv ed25519.PrivateKey, username string, now time.Time) string {
	var n [16]byte
	_, _ = rand.Read(n[:])
	body, _ := json.Marshal(struct {
		Sub   string `json:"sub"`
		Aud   string `json:"aud"`
		IAT   int64  `json:"iat"`
		Nonce string `json:"nonce"`
	}{username, Audience, now.Unix(), hex.EncodeToString(n[:])})

	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, body))
}

// Client holds one identity's statefs.ai token and renews it as needed.
type Client struct {
	Base     string
	Username string
	Key      ed25519.PrivateKey
	HTTP     *http.Client
	Now      func() time.Time
	// UserAgent, e.g. "parley/0.3.18", lets statefs.ai show each agent's
	// client version and flag the ones below its floor.
	UserAgent string

	mu      sync.Mutex
	token   string
	expires time.Time
}

// renewBefore: sign in again this long before the token expires, so a
// request never carries one that lapses in flight.
const renewBefore = time.Minute

// People answers the confirmed people in the caller's organization matching
// q (at least 2 characters), with their agents' identities.
func (c *Client) People(ctx context.Context, q string, limit int) ([]Person, error) {
	path := "/api/v1/agent/people?q=" + url.QueryEscape(q) + "&limit=" + fmt.Sprint(limit)
	var out struct {
		People []Person `json:"people"`
	}
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}

	if out.People == nil {
		out.People = []Person{}
	}

	return out.People, nil
}

// Presence tells statefs.ai this session is listening or composing.
// Display only. A missing route or a down API is an error for the caller
// to swallow.
// Presence pings the agent presence route. participant is the handle this
// session speaks under, so a chip can name the seat ("reviewer") rather than
// the machine identity every seat on a laptop shares; it is omitted when the
// session declared no handle.
func (c *Client) Presence(ctx context.Context, session, state, participant string, namespaces []string) error {
	fields := map[string]any{
		"session":    session,
		"state":      state,
		"namespaces": namespaces,
	}
	if participant != "" {
		fields["participant"] = participant
	}
	body, _ := json.Marshal(fields)
	tok, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	code, raw, err := c.do(ctx, http.MethodPost, "/api/v1/agent/presence", tok, body)
	if err != nil {
		return err
	}
	if code == http.StatusOK || code == http.StatusNoContent {
		return nil
	}
	if code == http.StatusUnauthorized {
		return ErrSignIn
	}
	return refusal(code, raw)
}

// get calls an agent route; a 401 means the token was refused, so it signs
// in again once and retries.
func (c *Client) get(ctx context.Context, path string, out any) error {
	for attempt := 0; ; attempt++ {
		tok, err := c.bearer(ctx)
		if err != nil {
			return err
		}

		code, body, err := c.do(ctx, http.MethodGet, path, tok, nil)
		if err != nil {
			return err
		}

		if code == http.StatusUnauthorized && attempt == 0 {
			c.forget(tok)
			continue
		}

		if err := refusal(code, body); err != nil {
			return err
		}

		return json.Unmarshal(body, out)
	}
}

func (c *Client) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && c.now().Add(renewBefore).Before(c.expires) {
		return c.token, nil
	}

	in, _ := json.Marshal(map[string]string{"assertion": Sign(c.Key, c.Username, c.now())})
	code, body, err := c.do(ctx, http.MethodPost, "/api/v1/agent/token", "", in)
	if err != nil {
		return "", err
	}

	if code == http.StatusUnauthorized {
		return "", ErrSignIn
	}

	if err := refusal(code, body); err != nil {
		return "", err
	}

	var t struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.Token == "" {
		return "", fmt.Errorf("%w: sign-in answered no token", ErrUnavailable)
	}

	c.token, c.expires = t.Token, t.ExpiresAt
	return c.token, nil
}

// forget drops a refused token, unless another request already replaced it.
func (c *Client) forget(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token == tok {
		c.token = ""
	}
}

func (c *Client) do(ctx context.Context, method, path, token string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}

	res, err := hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, nil
}

// refusal maps an agent route's non-200 answer to an error.
func refusal(code int, body []byte) error {
	switch {
	case code == http.StatusOK:
		return nil
	case code == http.StatusUnauthorized:
		return ErrSignIn
	case code == http.StatusForbidden:
		return ErrLookupOff
	case code >= 500:
		return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, code)
	}

	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error == "" {
		e.Error = strings.TrimSpace(string(body))
	}

	return fmt.Errorf("statefs.ai: HTTP %d: %s", code, e.Error)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}

	return time.Now()
}
