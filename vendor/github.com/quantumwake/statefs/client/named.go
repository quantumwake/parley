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

// Head answers a namespace's row count (the next append position) as the
// member at memberURL sees it, via a one-row read on the data plane.
func (c *Client) Head(ctx context.Context, memberURL, namespace string) (int64, error) {
	var res types.ReadResult
	url := strings.TrimRight(memberURL, "/") + "/api/v1/state/" + namespace + "?offset=0&limit=1"
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

// ListGrants lists the tenant's grants (admin plane).
func (c *Client) ListGrants(ctx context.Context) ([]Grant, error) {
	var out struct {
		Grants []Grant `json:"grants"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tenant/grants", nil, &out); err != nil {
		return nil, err
	}

	return out.Grants, nil
}
