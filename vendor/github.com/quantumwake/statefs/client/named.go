package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
// asked for would make the member's engine load a whole sealed block into
// memory just to serve it — readBlockData reads the entire Parquet block into memory
// before ReadBlock slices one row out of it — and parley's console calls
// Head every 1.5 seconds per open conversation. A one-row Head read against
// a namespace whose first block was 117 MB is what OOM-killed a member on
// 2026-09-18. The engine's contract is readRows' early return on
// `offset >= totalRows || limit <= 0` (pkg/engine/read.go), which answers
// TotalRows before a block is listed or read; the member passes a literal
// limit=0 straight through (node/cmd/statefs-member/main.go, queryInt), so
// no member-side change is needed. pkg/engine/read_no_block_test.go guards
// that contract.
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
