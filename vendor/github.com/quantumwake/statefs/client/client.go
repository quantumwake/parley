// Package client is the statefs cluster CLIENT SDK: resolve a namespace
// through the directory, then read (scan) or append against the owning
// group's members over their HTTP API. It holds no state and depends on
// nothing beyond the standard library and the statefs types — the same
// wire protocol the console and loadgen speak, packaged for consumers.
//
// Scans STREAM: rows arrive page by page through a callback, never
// accumulated — a namespace of any size exports in constant memory.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quantumwake/statefs/pkg/ticket"
	"github.com/quantumwake/statefs/pkg/types"
)

// Client talks to one statefs cluster: the directory for routing, the
// group members for data.
type Client struct {
	// Directory is the directory service base URL ("" = member-direct
	// mode: Route is unavailable and calls must target explicit member
	// URLs).
	Directory string

	// Token is the cluster control token, sent as X-Cluster-Token when
	// non-empty. Reads on a default deployment do not need it.
	Token string

	// Bearer is a PRE-ISSUED acting token (or a legacy bearer) used as-is
	// on directory calls. Prefer Credentials — the SDK then exchanges and
	// refreshes acting tokens itself (RFC-0011 §12.2).
	Bearer string

	// Credentials is the durable authenticator (api key | password |
	// identity file) exchanged for short-lived acting tokens. Zero value
	// = operator/dev flows (cluster token only, no tickets).
	Credentials Credentials

	// HTTP is the transport; nil takes a 30s-timeout default. Callers in
	// ingress-fronted environments (kind) inject a custom dialer here —
	// see NewIngressHTTPClient.
	HTTP *http.Client

	// tickets caches data-plane grant tickets per namespace; acting
	// caches the exchanged token.
	tickets ticketCache
	acting  actingToken
}

// New builds a Client for a directory-fronted cluster.
//
//	directoryURL the directory service base URL (e.g. http://directory:8090).
//	token        control token; "" for open deployments.
//	hc           custom transport; nil = 30s-timeout default.
func New(
	directoryURL, token string,
	// --
	hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}

	return &Client{Directory: strings.TrimRight(directoryURL, "/"), Token: token, HTTP: hc}
}

// NewIngressHTTPClient builds an http.Client that dials every *.<domain>
// hostname through one ingress address (host header routing) — the local
// kind deployment's access pattern. Pass the result to New.
//
//	domain  hostname suffix routed by the ingress (e.g. "statefs.localhost").
//	ingress the address that actually serves those names (e.g. "127.0.0.1:80").
func NewIngressHTTPClient(
	domain, ingress string,
	// --
	timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if host, _, err := net.SplitHostPort(addr); err == nil && strings.HasSuffix(host, domain) {
					return dialer.DialContext(ctx, network, ingress)
				}

				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
}

// Target is a namespace's routing answer: the owning group and where to
// read/write it right now.
type Target struct {
	Namespace      string
	NodeID         string // the owning group
	BaseURL        string // the group's registered URL (fallback)
	Primary        string // the current leader's EXTERNAL URL ("" when leaderless)
	PrimaryControl string // the leader's in-cluster control URL (for in-cluster callers)
}

// InClusterReadURL prefers the leader's in-cluster control URL (pod DNS)
// over the external ingress host — for callers running INSIDE the cluster
// (the query service) that cannot resolve *.statefs.localhost names.
func (t Target) InClusterReadURL() string {
	if t.PrimaryControl != "" {
		return t.PrimaryControl
	}

	return t.ReadURL()
}

// ReadURL is the member endpoint a scan should page against: the leader
// when known (freshest), the group URL otherwise.
func (t Target) ReadURL() string {
	if t.Primary != "" {
		return t.Primary
	}

	return t.BaseURL
}

// Route resolves which group serves a namespace (read-only: never places).
func (c *Client) Route(ctx context.Context, namespace string) (Target, error) {
	if c.Directory == "" {
		return Target{}, fmt.Errorf("client: no directory URL configured")
	}

	var out struct {
		Namespace string `json:"namespace"`
		NodeID    string `json:"node_id"`
		BaseURL   string `json:"base_url"`
		Primary   *struct {
			BaseURL    string `json:"base_url"`
			ControlURL string `json:"control_url"`
		} `json:"primary"`
	}
	if err := c.getJSON(ctx, c.Directory+"/api/v1/cluster/route/"+namespace, &out); err != nil {
		return Target{}, err
	}

	t := Target{Namespace: namespace, NodeID: out.NodeID, BaseURL: out.BaseURL}
	if out.Primary != nil {
		t.Primary = out.Primary.BaseURL
		t.PrimaryControl = out.Primary.ControlURL
	}

	return t, nil
}

// ScanOptions bound a streaming scan.
type ScanOptions struct {
	Start int64 // first row (absolute position; 0 = the beginning)
	End   int64 // one past the last row; <= 0 = the namespace head at scan start
	Page  int64 // rows per request (default 4096)
}

// Scan pages [Start, End) of a namespace through fn, IN ORDER, streaming —
// no page is held after fn returns. fn receives each page's first absolute
// position and its records; returning an error stops the scan and
// surfaces that error. Returns the total rows delivered.
//
//	memberURL the member to read from (Target.ReadURL after Route, or a
//	          direct member URL).
//	namespace the log to scan.
//	opts      range and page size; the zero value scans everything.
//	fn        the per-page consumer.
func (c *Client) Scan(
	ctx context.Context,
	memberURL, namespace string,
	opts ScanOptions,
	// --
	fn func(offset int64, records []types.Record) error) (int64, error) {
	page := opts.Page
	if page <= 0 {
		page = 4096
	}

	base := strings.TrimRight(memberURL, "/") + "/api/v1/state/" + namespace
	cur := opts.Start
	if cur < 0 {
		cur = 0
	}

	end := opts.End
	var delivered int64

	for {
		// Clamp the request to the remaining range.
		limit := page
		if end > 0 && cur+limit > end {
			limit = end - cur
		}

		if end > 0 && limit <= 0 {
			return delivered, nil
		}

		var res types.ReadResult
		url := base + "?offset=" + strconv.FormatInt(cur, 10) + "&limit=" + strconv.FormatInt(limit, 10)
		if err := c.getJSONData(ctx, url, namespace, &res); err != nil {
			return delivered, fmt.Errorf("scan %s at %d: %w", namespace, cur, err)
		}

		normalizeNumbers(res.Records)

		// End unbounded: pin it to the head as of the FIRST answer so the
		// scan terminates under live writes.
		if end <= 0 {
			end = res.TotalRows
			if end <= cur {
				return delivered, nil
			}

			continue // re-issue with the clamped limit
		}

		if len(res.Records) == 0 {
			return delivered, nil // range ran past the readable head
		}

		if err := fn(cur, res.Records); err != nil {
			return delivered, err
		}

		delivered += int64(len(res.Records))
		cur += int64(len(res.Records))
		if cur >= end {
			return delivered, nil
		}
	}
}

// normalizeNumbers rewrites each record's json.Number values (getJSON
// decodes with UseNumber — see below) into int64 when integral, float64
// otherwise. Without this, every JSON number lands as float64 and int64
// values past 2^53 — sequence numbers, nanosecond timestamps — silently
// lose precision (1785086675490691000 became 1.785086675490691e+18).
func normalizeNumbers(records []types.Record) {
	for _, rec := range records {
		for k, v := range rec {
			num, ok := v.(json.Number)
			if !ok {
				continue
			}

			if i, err := num.Int64(); err == nil {
				rec[k] = i
				continue
			}

			if f, err := num.Float64(); err == nil {
				rec[k] = f
				continue
			}

			rec[k] = num.String()
		}
	}
}

// authorize stamps the customer credential (a live acting token) on a
// directory request; no credentials = nothing stamped.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	b, err := c.bearer(ctx)
	if err != nil {
		return err
	}

	if b != "" {
		req.Header.Set("Authorization", "Bearer "+b)
	}

	return nil
}

// getJSON is the shared GET + decode with token and error surfacing.
func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("X-Cluster-Token", c.Token)
	}

	if err := c.authorize(ctx, req); err != nil {
		return err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
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

// Resolve is the PLACING resolution (POST): a namespace nobody owns yet
// is pinned to a group by the directory's placement — the call a WRITER
// makes before its first append. Reads use Route instead (never places).
func (c *Client) Resolve(ctx context.Context, namespace string) (Target, error) {
	if c.Directory == "" {
		return Target{}, fmt.Errorf("client: no directory URL configured")
	}

	var out struct {
		Namespace string `json:"namespace"`
		NodeID    string `json:"node_id"`
		BaseURL   string `json:"base_url"`
		Primary   *struct {
			BaseURL string `json:"base_url"`
		} `json:"primary"`
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Directory+"/api/v1/cluster/resolve/"+namespace, nil)
	if err != nil {
		return Target{}, err
	}

	if c.Token != "" {
		req.Header.Set("X-Cluster-Token", c.Token)
	}

	if err := c.authorize(ctx, req); err != nil {
		return Target{}, err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Target{}, err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return Target{}, fmt.Errorf("resolve %s: HTTP %d: %s", namespace, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&out); err != nil {
		return Target{}, err
	}

	t := Target{Namespace: namespace, NodeID: out.NodeID, BaseURL: out.BaseURL}
	if out.Primary != nil {
		t.Primary = out.Primary.BaseURL
	}

	return t, nil
}

// Appender is a namespace write session: it knows the current leader and
// silently follows leadership as it moves (307 redirects, 503 + directory
// re-resolve). One Appender per namespace; not safe for concurrent use.
type Appender struct {
	c         *Client
	namespace string
	url       string // current leader base URL
	sync      bool   // request sync durability on every append
}

// AppenderOption tunes a write session.
type AppenderOption func(*Appender)

// WithSync makes every append in this session request SYNC durability
// (X-Durability header; RFC-0005 §11 strengthen-only — it can never
// weaken a namespace's floor).
func WithSync() AppenderOption { return func(a *Appender) { a.sync = true } }

// Appender opens a write session, placing the namespace if it is new.
func (c *Client) Appender(ctx context.Context, namespace string, opts ...AppenderOption) (*Appender, error) {
	// POST resolve/{id} is the operator plane (cluster token); a customer
	// credential routes through GET route/{id}, which its bearer may read.
	// Namespaces created through the directory are already placed, so the
	// read-only route is enough for a bearer's write path.
	var t Target
	var err error
	if c.Token != "" {
		t, err = c.Resolve(ctx, namespace)
	} else {
		t, err = c.Route(ctx, namespace)
	}

	if err != nil {
		return nil, err
	}

	url := t.ReadURL()
	if url == "" {
		return nil, fmt.Errorf("append %s: group %s has no reachable member", namespace, t.NodeID)
	}

	app := &Appender{c: c, namespace: namespace, url: url}
	for _, o := range opts {
		o(app)
	}

	return app, nil
}

// Append writes one batch of records at the tail, following leadership
// moves (bounded retries) — the caller just streams batches.
func (a *Appender) Append(ctx context.Context, records []types.Record) (types.AppendResult, error) {
	var res types.AppendResult
	body, err := json.Marshal(records)
	if err != nil {
		return res, err
	}

	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(a.url, "/")+"/api/v1/state/"+a.namespace, strings.NewReader(string(body)))
		if err != nil {
			return res, err
		}

		req.Header.Set("Content-Type", "application/json")
		if a.sync {
			req.Header.Set("X-Durability", "sync")
		}

		if err := a.c.signDataPlane(ctx, req, a.namespace, ticket.VerbWrite, body); err != nil {
			return res, err
		}

		resp, err := a.c.HTTP.Do(req)
		if err != nil {
			return res, err
		}

		switch resp.StatusCode {
		case http.StatusAccepted:
			// Sync requested; the leader is durable but the replica quorum
			// did not confirm in time. Honest, distinct error — the rows
			// exist and are replicating.
			defer resp.Body.Close()
			return res, ErrDurabilityNotConfirmed

		case http.StatusOK:
			defer resp.Body.Close()
			return res, json.NewDecoder(resp.Body).Decode(&res)

		case http.StatusTemporaryRedirect:
			// The follower told us who leads: go there directly.
			var ans struct {
				LeaderURL string `json:"leader_url"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&ans)
			resp.Body.Close()
			if ans.LeaderURL != "" {
				a.url = ans.LeaderURL
				continue
			}

			fallthrough // redirect without a leader URL: re-resolve

		case http.StatusUnauthorized:
			// A refused ticket (revoked, or expired mid-flight) earns ONE
			// refresh; a second 401 is an honest authorization failure.
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			if attempt > 0 || !a.c.hasCustomerCredential() {
				return res, fmt.Errorf("append %s: HTTP 401: %s", a.namespace, strings.TrimSpace(string(msg)))
			}

			a.c.dropTicket(a.namespace)
			a.c.dropBearer()

		case http.StatusServiceUnavailable:
			// Leadership in motion: ask the directory again, briefly later.
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
			}

			if t, err := a.c.Resolve(ctx, a.namespace); err == nil && t.ReadURL() != "" {
				a.url = t.ReadURL()
			}

		default:
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			return res, fmt.Errorf("append %s: HTTP %d: %s", a.namespace, resp.StatusCode, strings.TrimSpace(string(msg)))
		}
	}

	return res, fmt.Errorf("append %s: leadership did not settle after retries", a.namespace)
}

// ErrDurabilityNotConfirmed: a sync append is leader-durable and
// replicating, but the durable quorum did not confirm within the
// leader's timeout. Retrying re-appends (duplicate risk) — callers
// should verify or surface, not blindly retry.
var ErrDurabilityNotConfirmed = errors.New("client: sync durability not confirmed (leader-durable only)")

// SetDurability flips a namespace's durability floor (async|sync).
func (c *Client) SetDurability(ctx context.Context, namespace, mode string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/cluster/namespaces/"+namespace+"/durability",
		map[string]string{"mode": mode}, nil)
}

// GroupInfo is one replication group as the directory lists it.
type GroupInfo struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
}

// Groups lists the directory's registered replication groups.
func (c *Client) Groups(ctx context.Context) ([]GroupInfo, error) {
	if c.Directory == "" {
		return nil, fmt.Errorf("client: no directory URL configured")
	}

	var out []GroupInfo
	if err := c.getJSON(ctx, c.Directory+"/api/v1/cluster/nodes", &out); err != nil {
		return nil, err
	}

	return out, nil
}

// GroupMember is one member row of a group's roster.
type GroupMember struct {
	ID           string `json:"instance_id"`
	Role         string `json:"role"`
	Status       string `json:"status"`
	BaseURL      string `json:"base_url"`
	ControlURL   string `json:"control_url"`
	GRPCAddr     string `json:"grpc_addr"`
	WrittenTotal int64  `json:"written_total"`
}

// GroupInstancesView is a replication group's roster + current term —
// what read placement needs (lag, liveness, addresses).
type GroupInstancesView struct {
	NodeID    string        `json:"node_id"`
	Term      int64         `json:"term"`
	Instances []GroupMember `json:"instances"`
}

// GroupInstances answers a group's member roster (term + instances).
func (c *Client) GroupInstances(ctx context.Context, groupID string) (GroupInstancesView, error) {
	var v GroupInstancesView
	if c.Directory == "" {
		return v, fmt.Errorf("client: no directory URL configured")
	}

	err := c.getJSON(ctx, c.Directory+"/api/v1/cluster/groups/"+url.PathEscape(groupID)+"/instances", &v)
	return v, err
}

// NamespaceInfo is one namespace's directory-side listing row: pinned
// name plus the leader's last-reported footprint (Sized=false when the
// size has not been reported yet — capped heartbeat list).
type NamespaceInfo struct {
	Namespace   string `json:"namespace"`
	Sized       bool   `json:"sized"`
	Rows        int64  `json:"rows"`
	Blocks      int64  `json:"blocks"`
	BlockBytes  int64  `json:"block_bytes"`
	WALBytes    int64  `json:"wal_bytes"`
	TotalBytes  int64  `json:"total_bytes"`
	AvgRowBytes int64  `json:"avg_row_bytes"`
}

// GroupNamespaces lists a group's namespaces (pins ∪ reported sizes).
func (c *Client) GroupNamespaces(ctx context.Context, group string) ([]NamespaceInfo, error) {
	if c.Directory == "" {
		return nil, fmt.Errorf("client: no directory URL configured")
	}

	var out struct {
		Namespaces []NamespaceInfo `json:"namespaces"`
	}
	if err := c.getJSON(ctx, c.Directory+"/api/v1/cluster/groups/"+group+"/namespaces", &out); err != nil {
		return nil, err
	}

	return out.Namespaces, nil
}

// Enroll burns a single-use enrollment token and registers a public key
// on the identity the token was minted for (RFC-0011 §12.3; see
// docs/cluster/ENROLLMENT.md). No bearer is needed — the token is the
// proof. caps nil → the server default (read, write); alg "" → ed25519.
func (c *Client) Enroll(ctx context.Context, token, publicKey, alg, label string, caps []string) (map[string]any, error) {
	if token == "" || publicKey == "" {
		return nil, fmt.Errorf("client: enroll needs a token and a public key")
	}

	body := map[string]any{"token": token, "public_key": publicKey, "label": label}
	if alg != "" {
		body["alg"] = alg
	}

	if caps != nil {
		body["caps"] = caps
	}

	var out struct {
		Authenticator map[string]any `json:"authenticator"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/auth/enroll", body, &out); err != nil {
		return nil, err
	}

	return out.Authenticator, nil
}

// doJSON is the shared write-verb helper (POST/PATCH/DELETE + decode).
func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	if c.Directory == "" {
		return fmt.Errorf("client: no directory URL configured")
	}

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}

		body = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(ctx, method, c.Directory+path, body)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("X-Cluster-Token", c.Token)
	}

	if err := c.authorize(ctx, req); err != nil {
		return err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if out == nil {
		return nil
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// NamespaceMeta is a namespace's directory identity record.
type NamespaceMeta struct {
	Namespace   string         `json:"namespace"`
	NodeID      string         `json:"node_id"`
	PinnedAt    time.Time      `json:"pinned_at"`
	Scope       map[string]any `json:"scope"`
	Cordoned    bool           `json:"cordoned"`
	DisplayName string         `json:"display_name"` // RFC-0011 v2 human name ("" = unnamed)
	// RFC-0011 v2 ownership: the owning membership ("" = tenant-wide).
	OwnerMembershipID string `json:"owner_membership_id,omitempty"`
}

// CreateNamespace mints a namespace: name "" takes a server-side UUID;
// scope is the claim-like linkage (user id, firm, …) searched later.
func (c *Client) CreateNamespace(ctx context.Context, name string, scope map[string]any) (string, error) {
	return c.CreateNamespaceDurable(ctx, name, scope, "")
}

// CreateNamespaceDurable additionally sets the durability floor at birth.
func (c *Client) CreateNamespaceDurable(ctx context.Context, name string, scope map[string]any, durability string) (string, error) {
	var out struct {
		Namespace string `json:"namespace"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/cluster/namespaces",
		map[string]any{"namespace": name, "scope": scope, "durability": durability}, &out)
	return out.Namespace, err
}

// NamespaceMetaOf answers one namespace's identity record.
func (c *Client) NamespaceMetaOf(ctx context.Context, namespace string) (NamespaceMeta, error) {
	var out NamespaceMeta
	err := c.getJSON(ctx, c.Directory+"/api/v1/cluster/namespaces/"+namespace, &out)
	return out, err
}

// FindNamespaces lists namespaces whose scope CONTAINS every given
// key/value (a namespace scoped {"firm":"x","user":"u1"} matches
// {"firm":"x"}).
func (c *Client) FindNamespaces(ctx context.Context, scope map[string]any, limit int) ([]NamespaceMeta, error) {
	b, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}

	var out struct {
		Namespaces []NamespaceMeta `json:"namespaces"`
	}
	q := "/api/v1/cluster/namespaces?limit=" + strconv.Itoa(limit) + "&scope=" + url.QueryEscape(string(b))
	if err := c.getJSON(ctx, c.Directory+q, &out); err != nil {
		return nil, err
	}

	return out.Namespaces, nil
}

// EnrichScope merges keys into a namespace's scope (existing keys the
// caller does not name survive).
func (c *Client) EnrichScope(ctx context.Context, namespace string, scope map[string]any) (NamespaceMeta, error) {
	var out NamespaceMeta
	err := c.doJSON(ctx, http.MethodPatch, "/api/v1/cluster/namespaces/"+namespace,
		map[string]any{"scope": scope}, &out)
	return out, err
}

// DeleteNamespace tears a namespace down everywhere: data on every live
// member, then the pin + scope. Partial failures answer an error and
// keep the pin for retry.
func (c *Client) DeleteNamespace(ctx context.Context, namespace string) (map[string]string, error) {
	var out struct {
		Members map[string]string `json:"members"`
	}
	err := c.doJSON(ctx, http.MethodDelete, "/api/v1/cluster/namespaces/"+namespace, nil, &out)
	return out.Members, err
}

// Cordon freezes (or thaws) writes to a namespace: new writer resolves
// answer 423 at the directory, and live members refuse appends.
func (c *Client) Cordon(ctx context.Context, namespace string, cordoned bool) error {
	verb := "/cordon"
	if !cordoned {
		verb = "/uncordon"
	}

	return c.doJSON(ctx, http.MethodPost, "/api/v1/cluster/namespaces/"+namespace+verb, nil, nil)
}
