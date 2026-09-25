package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quantumwake/statefs/pkg/types"
)

// CreateNamespaceOpts is the full birth record a bearer may set (RFC-0011
// v2): the human display name is searchable and unique within the tenant.
type CreateNamespaceOpts struct {
	Namespace   string         // "" takes a server-side UUID
	DisplayName string         // human name; "" = unnamed
	Scope       map[string]any // search blob; tags:[...] by convention
	Durability  string         // "" | async | sync (floor at birth)
}

// CreatedNamespace is the directory's answer to a create.
type CreatedNamespace struct {
	Namespace   string         `json:"namespace"`
	DisplayName string         `json:"display_name"`
	Scope       map[string]any `json:"scope"`
	NodeID      string         `json:"node_id"`
}

// CreateNamespaceWith mints a namespace with a display name. A duplicate
// display name within the tenant is refused by the directory (HTTP 409);
// IsConflict recognizes that error so callers can fall back to a lookup.
func (c *Client) CreateNamespaceWith(ctx context.Context, opts CreateNamespaceOpts) (CreatedNamespace, error) {
	var out CreatedNamespace
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/cluster/namespaces", map[string]any{
		"namespace": opts.Namespace, "display_name": opts.DisplayName,
		"scope": opts.Scope, "durability": opts.Durability,
	}, &out)
	return out, err
}

// FindNamespacesByName answers the tenant-bounded display-name search
// (`?q=`); q matches names case-insensitively as a substring.
func (c *Client) FindNamespacesByName(ctx context.Context, q string, limit int) ([]NamespaceMeta, error) {
	var out struct {
		Namespaces []NamespaceMeta `json:"namespaces"`
	}
	path := "/api/v1/cluster/namespaces?limit=" + strconv.Itoa(limit) + "&q=" + url.QueryEscape(q)
	if err := c.getJSON(ctx, c.Directory+path, &out); err != nil {
		return nil, err
	}

	return out.Namespaces, nil
}

// FindNamespacesByOwner answers the namespaces one seat (a membership id)
// created, optionally narrowed by a display-name search — the "Owns"
// view. Paged: limit rows from offset.
func (c *Client) FindNamespacesByOwner(ctx context.Context, ownerMembershipID, q string, limit, offset int) ([]NamespaceMeta, error) {
	var out struct {
		Namespaces []NamespaceMeta `json:"namespaces"`
	}
	path := "/api/v1/cluster/namespaces?limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(offset) +
		"&q=" + url.QueryEscape(q) + "&owner=" + url.QueryEscape(ownerMembershipID)
	if err := c.getJSON(ctx, c.Directory+path, &out); err != nil {
		return nil, err
	}

	return out.Namespaces, nil
}

// Head answers a namespace's row count (the next append position) as the
// member at memberURL sees it. It asks for ZERO rows on purpose: any row it
// asked for would make the member's engine open a sealed block just to serve
// it, and parley's console calls Head every 1.5 seconds per open
// conversation. A one-row Head read against a namespace whose first block
// was 117 MB is what OOM-killed a member on 2026-09-18 — the file-backed
// block read (RFC-0022 D-M6, pkg/engine/read.go) has since made that one row
// cheap, but "cheap" is not "free", and asking for nothing is still the only
// ask that touches no block at all. The engine's contract is readRows' early
// return on `offset >= totalRows || limit <= 0` (pkg/engine/read.go), which
// answers TotalRows before a block is listed or read; the member passes a
// literal limit=0 straight through (node/cmd/statefs-member/state_routes.go,
// queryInt — the READ_MAX_ROWS clamp only ever lowers a limit, never raises
// one). pkg/engine/read_no_block_test.go guards that contract.
func (c *Client) Head(ctx context.Context, memberURL, namespace string) (int64, error) {
	var res types.ReadResult
	url := strings.TrimRight(memberURL, "/") + "/api/v1/state/" + namespace + "?offset=0&limit=0"
	if err := c.getJSONData(ctx, url, namespace, &res); err != nil {
		return 0, err
	}

	return res.TotalRows, nil
}

// IsConflict reports whether err is the directory's HTTP 409 answer (a
// display name already taken).
func IsConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 409")
}

// IsNotFound reports whether err is an HTTP 404 answer.
func IsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

// IsUnauthenticated reports whether err is an HTTP 401 answer — the
// credential itself was refused (rejected, or a token that expired) — as
// opposed to a refusal of ACCESS (403, 423). It lives here, beside
// IsRefused, so callers branch on the client's own knowledge of its error
// wording instead of each matching "HTTP 401" themselves: parley's wait
// needs exactly this split, because a 401 can be cured by exchanging the
// credential again and a 403 cannot.
func IsUnauthenticated(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 401")
}

// IsRefused reports whether err is an HTTP 401/403/423 answer (no grant,
// expired credential, or a cordoned namespace).
func IsRefused(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "HTTP 423")
}

// OverBudgetError is the member's 503 answer carrying code
// "over_memory_budget" (2026-09-18 incident) OR "over_write_budget"
// (RFC-0022 D-M8: the member-wide unsealed-bytes budget is already spent by
// other appends) — the two are wrapped identically since both are the same
// shape of refusal (transient, retry-after-bounded, never retried here).
// RetryAfter names how long the member itself suggested waiting (its own
// Retry-After response header; 0 if it sent none or this wasn't parsed from
// one). This package never retries it automatically: a 503 is otherwise
// "leadership in motion" on the append path (client.go), and
// re-resolving/retrying a budget refusal would be wasted work that also
// hides the refusal from the caller — so wherever a 503 might carry either
// code, the code is read FIRST and, when it matches, wrapped as
// *OverBudgetError and returned at once instead of falling into that
// leadership retry. The caller owns whatever policy comes next.
type OverBudgetError struct {
	Err        error // the "HTTP 503: ..." text every refusal in this package carries
	RetryAfter time.Duration
}

func (e *OverBudgetError) Error() string { return e.Err.Error() }
func (e *OverBudgetError) Unwrap() error { return e.Err }

// refusalBodyLimit bounds how much of a refusal body this package reads
// before parsing it. It is deliberately far larger than the 256 characters
// the human-readable message is truncated to: parseCapCode json.Unmarshals
// those same bytes, and a body cut mid-object does not parse AT ALL, so
// reading only 256 would silently turn any refusal with a longer message
// back into an uncoded one — the exact failure the code field exists to
// prevent. Still bounded, because a refusal body is attacker-influenced
// input on the read path.
const refusalBodyLimit = 8 << 10

// readRefusalBody reads a refusal response's body (bounded by
// refusalBodyLimit) and answers both the raw bytes, for parseCapCode, and
// the trimmed, 256-character message this package puts in an error string.
func readRefusalBody(body io.Reader) (raw []byte, message string) {
	raw, _ = io.ReadAll(io.LimitReader(body, refusalBodyLimit))
	message = strings.TrimSpace(string(raw))
	if len(message) > 256 {
		message = message[:256]
	}

	return raw, message
}

// parseCapCode reads a refusal body for the stable "code" field the
// member's cap/budget refusals carry (node/cmd/statefs-member/
// state_routes.go's writeCapError) — "" when the body does not decode or
// carries none, which is any older member's plain {"error"} body, or a
// refusal that never had a code to begin with.
func parseCapCode(body []byte) string {
	var parsed struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	return parsed.Code
}

// parseRetryAfter reads Retry-After as the bare integer-seconds form the
// member always sends (never the HTTP-date form); 0 when absent or
// unparsable.
func parseRetryAfter(header http.Header) time.Duration {
	raw := header.Get("Retry-After")
	if raw == "" {
		return 0
	}

	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0
	}

	return time.Duration(seconds) * time.Second
}

// wrapIfOverBudget answers err unchanged unless status is 503 and body
// carries code "over_memory_budget" or "over_write_budget" (RFC-0022
// D-M8), in which case it wraps err as an *OverBudgetError (see its doc
// comment for why every 503 call site checks this before doing anything
// else with the status).
func wrapIfOverBudget(err error, status int, header http.Header, body []byte) error {
	if status != http.StatusServiceUnavailable {
		return err
	}

	switch parseCapCode(body) {
	case "over_memory_budget", "over_write_budget":
	default:
		return err
	}

	return &OverBudgetError{Err: err, RetryAfter: parseRetryAfter(header)}
}

// RetryAfter answers the duration the member suggested waiting before
// retrying an *OverBudgetError (its own Retry-After response header,
// parsed by wrapIfOverBudget), and whether err was in fact one with a
// usable value. A caller that gets ok=false and still wants to retry an
// IsOverBudget error picks its own backoff — this package suggests
// nothing on its own.
func RetryAfter(err error) (time.Duration, bool) {
	var budget *OverBudgetError
	if !errors.As(err, &budget) || budget.RetryAfter <= 0 {
		return 0, false
	}

	return budget.RetryAfter, true
}

// IsTooLarge reports whether err is an HTTP 413 answer: an append over
// APPEND_MAX_BYTES, APPEND_MAX_ROWS, or APPEND_MAX_ROW_BYTES (member's own
// JSON body, code "append_too_large"/"append_too_many_rows"/"row_too_large"
// — RFC-0022 D-M9 added the last one), or a body an in-front proxy refused
// before it ever reached a member (an HTML page, no "code" field — see
// deploy/charts/statefs-group/templates/ingress.yaml's proxy-body-size
// comment for why the two should usually agree on the same limit). Matches
// on the status alone, since only the member's own answer carries a body
// a client can parse further.
func IsTooLarge(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 413")
}

// IsOverBudget reports whether err is one of the member's two budget
// refusals: HTTP 503 carrying code "over_memory_budget" (2026-09-18
// incident, the block-load budget) or "over_write_budget" (RFC-0022 D-M8,
// the member-wide unsealed-bytes budget) — both transient, unlike
// IsNotServable. The member answers Retry-After: 1 for either; this
// package adds no automatic retry (a caller decides), and does not
// distinguish which of the two budgets refused — RetryAfter and the retry
// policy are identical either way.
//
// It matches ONLY the typed *OverBudgetError wrapIfOverBudget builds from
// one of those codes, never a bare "HTTP 503" string. A 503 is this
// cluster's LEADERSHIP-IN-MOTION answer as well ("leader unknown —
// re-resolve via the directory and retry",
// node/cmd/statefs-member/state_routes.go), and a string match would call
// that a memory refusal: a caller branching on IsOverBudget would then
// back off on its Retry-After instead of re-resolving, and would sit out a
// failover it should have followed. There is nothing to be lenient FOR,
// either — a member old enough to omit the code is also old enough never
// to answer either budget code at all.
func IsOverBudget(err error) bool {
	var budget *OverBudgetError
	return errors.As(err, &budget)
}

// IsNotServable reports whether err is an HTTP 507 answer: the block this
// read needed is itself larger than the member's WHOLE block-load memory
// budget (code "block_exceeds_budget") — unlike IsOverBudget, retrying
// against the SAME member can never succeed; a caller must either resize
// the budget (BLOCK_LOAD_BUDGET_BYTES) or read a smaller page.
func IsNotServable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 507")
}

// ActingToken exchanges the configured credentials (or returns the cached
// live token) and hands the acting token to callers that need to present
// it themselves, such as a plugin proving an enrollment worked. Prefer
// letting the client attach it; this exists for diagnostics and handoff.
func (c *Client) ActingToken(ctx context.Context) (string, error) {
	return c.bearer(ctx)
}

// Grant is one namespace access row on a member's seat in the tenant.
type Grant struct {
	Username  string `json:"username"`
	Namespace string `json:"namespace"`
	Access    string `json:"access"` // read | write
}

// GrantNamespace gives a member read or write on a namespace of the tenant
// (tenant admin plane: a manage-capable, admin credential).
func (c *Client) GrantNamespace(ctx context.Context, username, namespace, access string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/tenant/grants",
		map[string]string{"username": username, "namespace": namespace, "access": access}, nil)
}

// RevokeNamespace removes a member's grant on a namespace.
func (c *Client) RevokeNamespace(ctx context.Context, username, namespace string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/tenant/grants?username="+url.QueryEscape(username)+"&namespace="+url.QueryEscape(namespace), nil, nil)
}

// ListGrants lists the grants the caller may see: every grant for an
// admin, the grants on namespaces the caller owns otherwise.
func (c *Client) ListGrants(ctx context.Context) ([]Grant, error) {
	return c.ListGrantsWhere(ctx, "", "")
}

// ListGrantsWhere narrows ListGrants to one namespace and/or one member
// (username); an empty filter matches everything. The filter never
// widens the view — it runs after the server's wall.
func (c *Client) ListGrantsWhere(ctx context.Context, namespace, username string) ([]Grant, error) {
	var out struct {
		Grants []Grant `json:"grants"`
	}
	path := "/api/v1/tenant/grants"
	if namespace != "" || username != "" {
		path += "?namespace=" + url.QueryEscape(namespace) + "&username=" + url.QueryEscape(username)
	}

	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}

	return out.Grants, nil
}

// Tenant is a cluster-plane tenant row.
type Tenant struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Name        string `json:"name,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// Tenants lists every tenant (cluster plane: operator door).
func (c *Client) Tenants(ctx context.Context) ([]Tenant, error) {
	var out struct {
		Tenants []Tenant `json:"tenants"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/cluster/tenants", nil, &out); err != nil {
		return nil, err
	}

	return out.Tenants, nil
}

// CreateTenant creates a tenant and, when adminUser is set, its first
// admin (a person with a password). Answers the tenant row.
func (c *Client) CreateTenant(ctx context.Context, slug, name, adminUser, adminPassword string) (map[string]any, error) {
	body := map[string]any{"slug": slug, "display_name": name}
	if adminUser != "" {
		body["admin"] = map[string]string{"username": adminUser, "password": adminPassword}
	}

	var out map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/cluster/tenants", body, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// DeleteTenant removes a tenant (cluster plane).
func (c *Client) DeleteTenant(ctx context.Context, slug string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/cluster/tenants/"+url.PathEscape(slug), nil, nil)
}
