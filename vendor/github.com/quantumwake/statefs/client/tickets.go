// Data-plane grant tickets (RFC-0011, O-B+): when the deployment mints
// them, every append/scan to a member carries a short-TTL signed ticket
// plus a request MAC — fetched from the directory once per namespace per
// TTL, cached, and attached transparently. Deployments without minting
// (dev rigs) behave exactly as before: the first probe answers 404 and
// signing stays off for the client's lifetime.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/quantumwake/statefs/pkg/ticket"
)

// ticketCache holds fetched tickets per namespace (guarded; the Client
// is used concurrently).
type ticketCache struct {
	mu       sync.Mutex
	byNS     map[string]cachedTicket
	disabled bool // directory answered "minting not enabled"
}

// cachedTicket is one namespace's live grant.
type cachedTicket struct {
	raw string
	exp time.Time
}

// signDataPlane attaches the ticket header set to a member request.
// No bearer configured, or a directory without minting = no-op.
func (c *Client) signDataPlane(ctx context.Context, req *http.Request, namespace, verb string, body []byte) error {
	if !c.hasCustomerCredential() || c.Directory == "" {
		return nil
	}

	raw, err := c.grantTicket(ctx, namespace, verb)
	if err != nil {
		return err
	}

	if raw == "" {
		return nil // minting disabled on this deployment
	}

	return ticket.SignRequest(req, raw, body, time.Now())
}

// grantTicket answers a live ticket for a namespace covering ONE verb
// (asking for exactly what the operation needs is what lets a read-only
// grant scan), fetching or refreshing (30s before expiry) through the
// directory as needed. "" with a nil error = the deployment mints none.
func (c *Client) grantTicket(ctx context.Context, namespace, verb string) (string, error) {
	key := namespace + "|" + verb
	c.tickets.mu.Lock()
	if c.tickets.disabled {
		c.tickets.mu.Unlock()
		return "", nil
	}

	if t, ok := c.tickets.byNS[key]; ok && time.Now().Before(t.exp.Add(-30*time.Second)) {
		c.tickets.mu.Unlock()
		return t.raw, nil
	}
	c.tickets.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.Directory+"/auth/ticket?ns="+url.QueryEscape(namespace)+"&verbs="+verb, nil)
	if err != nil {
		return "", err
	}

	if err := c.authorize(ctx, req); err != nil {
		return "", err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	// 404 = this deployment mints no tickets; remember and stop asking.
	if resp.StatusCode == http.StatusNotFound {
		c.tickets.mu.Lock()
		c.tickets.disabled = true
		c.tickets.mu.Unlock()
		return "", nil
	}

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", fmt.Errorf("ticket %s: HTTP %d: %s", namespace, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Ticket string `json:"ticket"`
		Exp    int64  `json:"exp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}

	c.tickets.mu.Lock()
	if c.tickets.byNS == nil {
		c.tickets.byNS = map[string]cachedTicket{}
	}

	c.tickets.byNS[key] = cachedTicket{raw: out.Ticket, exp: time.Unix(out.Exp, 0)}
	c.tickets.mu.Unlock()
	return out.Ticket, nil
}

// getJSONData is the member data-plane GET: ticket-signed when the
// deployment mints, with ONE refresh-and-retry on 401 (a grant can be
// revoked mid-TTL) — a second 401 surfaces honestly.
func (c *Client) getJSONData(ctx context.Context, url, namespace string, out any) error {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}

		req.Header.Set("Accept", "application/json")
		if err := c.signDataPlane(ctx, req, namespace, ticket.VerbRead, nil); err != nil {
			return err
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && c.hasCustomerCredential() {
			resp.Body.Close()
			c.dropTicket(namespace)
			c.dropBearer()
			continue
		}

		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			return fmt.Errorf("%s answered HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(msg)))
		}

		dec := json.NewDecoder(resp.Body)
		dec.UseNumber() // exact int64s — float64 corrupts values past 2^53
		return dec.Decode(out)
	}
}

// hasCustomerCredential answers whether this client acts as a customer
// identity at all (pre-issued bearer or exchangeable credentials).
func (c *Client) hasCustomerCredential() bool {
	return c.Bearer != "" || c.Credentials.Configured()
}

// dropTicket forgets a namespace's cached tickets (after a member 401 —
// e.g. a grant revoked mid-TTL) so the next attempt re-fetches.
func (c *Client) dropTicket(namespace string) {
	c.tickets.mu.Lock()
	for k := range c.tickets.byNS {
		if strings.HasPrefix(k, namespace+"|") {
			delete(c.tickets.byNS, k)
		}
	}
	c.tickets.mu.Unlock()
}
