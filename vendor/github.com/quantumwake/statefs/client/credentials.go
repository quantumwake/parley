// Credentials (RFC-0011 §12.2/§12.4): the SDK holds ONE durable
// authenticator — an api key, a person's password, or an identity file
// whose private key never leaves this machine — and exchanges it at the
// directory for short-lived per-tenant ACTING TOKENS, refreshed
// invisibly before expiry. No refresh tokens: re-exchange IS the refresh.
//
// Resolution ladder (boto3-style; first hit wins):
//  1. explicit fields on the Client (APIKey / Username+Password / KeyFile)
//  2. environment: STATEFS_API_KEY | STATEFS_KEY_FILE | STATEFS_PRIVATE_KEY
//     (inline identity-file JSON for CI), plus STATEFS_TENANT
//  3. the default identity file, ~/.statefs/identity
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/quantumwake/statefs/pkg/assertion"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// Credentials is the durable authenticator the client exchanges. Zero
// value = none (operator/dev flows: cluster token only, no tickets).
type Credentials struct {
	APIKey   string
	Username string
	Password string
	KeyFile  string             // path to an identity file
	Identity *identityfile.File // an already-loaded identity (inline env form)
	Tenant   string             // acting tenant; required when the identity sits in several
}

// Configured answers whether any authenticator is present.
func (c Credentials) Configured() bool {
	return c.APIKey != "" || (c.Username != "" && c.Password != "") || c.KeyFile != "" || c.Identity != nil
}

// CredentialsFromEnv resolves rungs 2 and 3 of the ladder.
func CredentialsFromEnv() Credentials {
	c := Credentials{APIKey: os.Getenv("STATEFS_API_KEY"), KeyFile: os.Getenv("STATEFS_KEY_FILE"), Tenant: os.Getenv("STATEFS_TENANT")}
	if inline := os.Getenv("STATEFS_PRIVATE_KEY"); inline != "" {
		if f, err := identityfile.Parse(inline); err == nil {
			c.Identity = &f
		}
	}

	if c.Configured() {
		return c
	}

	if _, err := os.Stat(identityfile.DefaultPath()); err == nil {
		c.KeyFile = identityfile.DefaultPath()
	}

	return c
}

// actingToken is the cached exchange result.
type actingToken struct {
	mu    sync.Mutex
	token string
	exp   time.Time
	// disabled: the directory has no exchange (pre-identity deployment)
	// — the durable credential is sent as the bearer, as before.
	disabled bool
}

// bearer answers the credential to put on Authorization: a pre-issued
// Bearer as-is, else a live acting token (exchanging as needed), else
// "" (no customer credential configured).
func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.Bearer != "" {
		return c.Bearer, nil
	}

	if !c.Credentials.Configured() {
		return "", nil
	}

	c.acting.mu.Lock()
	defer c.acting.mu.Unlock()
	if c.acting.disabled {
		return c.Credentials.APIKey, nil
	}

	if c.acting.token != "" && time.Now().Before(c.acting.exp.Add(-30*time.Second)) {
		return c.acting.token, nil
	}

	token, exp, err := c.exchange(ctx)
	if err != nil {
		return "", err
	}

	c.acting.token, c.acting.exp = token, exp
	return token, nil
}

// dropBearer forgets the acting token (a 401 mid-TTL — revoked seat).
func (c *Client) dropBearer() {
	c.acting.mu.Lock()
	c.acting.token = ""
	c.acting.mu.Unlock()
}

// exchange presents the durable credential at /auth/token. Caller holds
// acting.mu.
func (c *Client) exchange(ctx context.Context) (string, time.Time, error) {
	if c.Directory == "" {
		return "", time.Time{}, errors.New("client: no directory URL configured")
	}

	body, err := c.exchangeBody()
	if err != nil {
		return "", time.Time{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Directory+"/auth/token", strings.NewReader(string(body)))
	if err != nil {
		return "", time.Time{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}

	defer resp.Body.Close()

	// No exchange on this deployment: fall back to the key-as-bearer
	// contract for the client's lifetime.
	if resp.StatusCode == http.StatusNotFound && c.Credentials.APIKey != "" {
		c.acting.disabled = true
		return c.Credentials.APIKey, time.Now().Add(365 * 24 * time.Hour), nil
	}

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", time.Time{}, fmt.Errorf("token exchange: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, err
	}

	return out.Token, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

// exchangeBody renders the authenticator in precedence order: identity
// file (signed assertion) → api key → password.
func (c *Client) exchangeBody() ([]byte, error) {
	body := map[string]string{}
	if c.Credentials.Tenant != "" {
		body["tenant"] = c.Credentials.Tenant
	}

	switch {
	case c.Credentials.Identity != nil || c.Credentials.KeyFile != "":
		f := c.Credentials.Identity
		if f == nil {
			loaded, err := identityfile.Read(c.Credentials.KeyFile)
			if err != nil {
				return nil, err
			}

			f = &loaded
		}

		priv, err := f.Private()
		if err != nil {
			return nil, err
		}

		body["assertion"] = assertion.Sign(priv, f.Username, time.Now())
	case c.Credentials.APIKey != "":
		body["api_key"] = c.Credentials.APIKey
	case c.Credentials.Username != "":
		body["username"], body["password"] = c.Credentials.Username, c.Credentials.Password
	default:
		return nil, errors.New("client: no credentials configured")
	}

	return json.Marshal(body)
}
