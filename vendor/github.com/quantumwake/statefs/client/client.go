// Package client is the statefs cluster CLIENT SDK: resolve a namespace
// through the directory, then read (scan) or append against the owning
// group's members over their HTTP API. It holds no state and depends on
// nothing beyond the standard library and the statefs types — the same
// wire protocol the console and loadgen speak, packaged for consumers.
//
// Scans STREAM: rows arrive page by page through a callback, never
// accumulated — a namespace of any size exports in constant memory.
//
// The index read surface (RFC-0018 §5, index.go) adds six calls, all
// routed and ticket-gated exactly like Scan: Indexes (state per index),
// Lookup (equality), Range (an ordered range), Search (vector nearest
// neighbours), Rows (positions to records), and SetIndexes, which
// declares a namespace's indexes through the directory.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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

	// Cache, when set, is where this client keeps answers that outlive the
	// process — the namespace's route and its grant tickets (cache.go).
	// nil is the default and means exactly the behaviour this client had
	// before caching existed: every process asks the directory again.
	//
	// It MUST be scoped to one identity; NewDiskCache takes the identity
	// for that reason. A ticket is also a MAC key, so two identities that
	// shared a cache would be handing each other credentials.
	Cache Cache

	// HTTP is the transport; nil takes a 30s-timeout default. Callers in
	// ingress-fronted environments (kind) inject a custom dialer here —
	// see NewIngressHTTPClient.
	HTTP *http.Client

	// InCluster makes routed data calls prefer a member's in-cluster
	// control URL over its external ingress host. Set it in a caller that
	// runs inside the cluster, such as the query service, which cannot
	// resolve the external names; leave it unset everywhere else.
	InCluster bool

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

	hc = noTransportRedirects(hc)
	return &Client{Directory: strings.TrimRight(directoryURL, "/"), Token: token, HTTP: hc}
}

// noTransportRedirects makes the transport hand a 307 back to the caller
// instead of following it (OPS-3, code review 2026-09-21). A member that no
// longer leads answers a data-plane request with 307 plus a Location for
// the leader; Go's client followed that itself, re-sent the whole body to
// the leader and returned the leader's 200, so Appender's own redirect
// branch never ran, its URL never moved, and every later batch went to
// the follower first and the leader second — twice the upload, for the
// rest of the Appender's life. With the redirect returned as a response,
// Appender adopts the leader and the next batch goes there once. The
// directory's API never redirects, so nothing else changes. A client the
// caller built with its own CheckRedirect keeps it.
//
// The caller's *http.Client is never mutated: a shallow copy carries the
// Transport pointer (so connection pooling is unaffected) and only the copy
// stops following redirects. A caller that reuses one client for other
// services keeps its redirects there (champion's review of #156).
func noTransportRedirects(hc *http.Client) *http.Client {
	if hc.CheckRedirect != nil {
		return hc
	}

	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
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
	// Replicas are the group's live replicas, freshest first, as the
	// directory last heard from them. Empty on a deployment without
	// replication groups, and on a directory older than this field.
	Replicas []Replica
}

// Replica is one replica member of a target's group.
type Replica struct {
	BaseURL    string
	ControlURL string
	// Position is the namespace's replicated position this replica had
	// reported when the route was answered: A HINT, not a promise. 0 means
	// either an empty namespace or a replica that has reported nothing yet
	// (one still joining) — indistinguishable here, so a caller that must
	// not read an unreplicated namespace passes atLeast >= 1, which
	// excludes both. It is
	// as old as that member's last heartbeat, and only the member itself
	// can enforce a position bound at read time (P9/RFC-0007). Treat it as
	// "probably at least this far", never as "certainly".
	Position int64
	Suspect  bool // the directory doubts its liveness, but has not given up on it
}

// ReadMode says which members of a group a read may use. A namespace's
// replicas trail its primary, so a mode other than ReadPrimaryOnly means
// accepting a read that may be behind a write the caller has already made.
type ReadMode string

const (
	// ReadPrimaryOnly reads the leader. The default, and the only mode
	// that is read-your-writes without asking the member to enforce a
	// position.
	ReadPrimaryOnly ReadMode = "primary-only"
	// ReadReplicaFirst prefers a replica and falls back to the leader when
	// no eligible replica exists.
	ReadReplicaFirst ReadMode = "replica-first"
	// ReadAny treats the leader as one more eligible member, spreading a
	// read fleet over the whole group.
	ReadAny ReadMode = "any"
)

// ReadURLFor is the member URL a read should use under mode, choosing AT
// RANDOM among the eligible members rather than in turn.
//
// Random, not round-robin, because the callers are mostly short-lived
// processes: a client that resolves a route, reads, and exits would restart
// a round-robin counter at the same member every time, and the whole fleet
// would land on one replica. Random needs no state that outlives the
// process, and spreads evenly across many of them.
//
// atLeast is a position the read must not be behind — pass what the caller
// already knows it has written, or 0 when any position will do. A replica
// whose reported position is short of it is not eligible: the number is a
// hint (see Replica.Position), so this narrows the choice but does not make
// the read safe by itself. A caller that must not read stale data asks the
// member to enforce the bound, or uses ReadPrimaryOnly.
//
// It never answers "": with no eligible replica it falls back to the
// leader, and with no leader to the group URL, exactly as ReadURL does.
//
// These are EXTERNAL URLs. A caller inside the cluster, which cannot resolve
// the ingress hosts, uses InClusterReadURLFor.
func (t Target) ReadURLFor(mode ReadMode, atLeast int64) string {
	return t.pickRead(mode, atLeast, Replica.externalURL, t.ReadURL)
}

// InClusterReadURLFor is ReadURLFor for a caller running INSIDE the
// cluster (the query service, client/index.go's member reads): the same
// modes and the same random choice, over each member's in-cluster control
// URL where it has one, as InClusterReadURL is for the leader.
func (t Target) InClusterReadURLFor(mode ReadMode, atLeast int64) string {
	return t.pickRead(mode, atLeast, Replica.inClusterURL, t.InClusterReadURL)
}

func (r Replica) externalURL() string { return r.BaseURL }

func (r Replica) inClusterURL() string {
	if r.ControlURL != "" {
		return r.ControlURL
	}

	return r.BaseURL
}

// pickRead is ReadURLFor's choice, over whichever URL of each member the
// caller can reach: urlOf for a replica, leader for the leader.
func (t Target) pickRead(mode ReadMode, atLeast int64, urlOf func(Replica) string, leader func() string) string {
	if mode != ReadReplicaFirst && mode != ReadAny {
		return leader()
	}

	eligible := make([]string, 0, len(t.Replicas)+1)
	for _, r := range t.Replicas {
		if r.Suspect || r.Position < atLeast || urlOf(r) == "" {
			continue
		}

		eligible = append(eligible, urlOf(r))
	}

	// The leader is eligible under ReadAny, and is the fallback under
	// ReadReplicaFirst — where it is added only when no replica qualifies,
	// so "replica first" means what it says.
	if mode == ReadAny || len(eligible) == 0 {
		if url := leader(); url != "" {
			eligible = append(eligible, url)
		}
	}

	if len(eligible) == 0 {
		return leader()
	}

	return eligible[rand.IntN(len(eligible))]
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

// resolveForCredential answers the namespace's target on the plane this
// client can use: Resolve (operator plane, may place an unpinned namespace)
// with a cluster token, Route (read-only) otherwise. Appender's first URL
// and its re-route after a member's 503 both go through here, so a
// customer credential heals the same way an operator one does.
func (c *Client) resolveForCredential(ctx context.Context, namespace string) (Target, error) {
	if c.Token != "" {
		return c.Resolve(ctx, namespace)
	}

	return c.Route(ctx, namespace)
}

// Route resolves which group serves a namespace (read-only: never places).
//
// With a Cache set, an answer from an earlier process is used instead of
// asking the directory: a route changes when leadership moves, and the
// 307 that says so drops the entry (dropRoute), so a stale one costs one
// redirect and never a wrong answer. Without a Cache this is unchanged.
func (c *Client) Route(ctx context.Context, namespace string) (Target, error) {
	if c.Directory == "" {
		return Target{}, fmt.Errorf("client: no directory URL configured")
	}

	if t, ok := c.cachedRoute(namespace); ok {
		return t, nil
	}

	var out struct {
		Namespace string `json:"namespace"`
		NodeID    string `json:"node_id"`
		BaseURL   string `json:"base_url"`
		Primary   *struct {
			BaseURL    string `json:"base_url"`
			ControlURL string `json:"control_url"`
		} `json:"primary"`
		Replicas []struct {
			BaseURL    string `json:"base_url"`
			ControlURL string `json:"control_url"`
			Status     string `json:"status"`
			Position   int64  `json:"position"`
		} `json:"replicas"`
	}
	if err := c.getJSON(ctx, c.Directory+"/api/v1/cluster/route/"+namespace, &out); err != nil {
		return Target{}, err
	}

	t := Target{Namespace: namespace, NodeID: out.NodeID, BaseURL: out.BaseURL}
	if out.Primary != nil {
		t.Primary = out.Primary.BaseURL
		t.PrimaryControl = out.Primary.ControlURL
	}

	for _, r := range out.Replicas {
		t.Replicas = append(t.Replicas, Replica{
			BaseURL: r.BaseURL, ControlURL: r.ControlURL,
			Position: r.Position, Suspect: r.Status == "SUSPECT",
		})
	}

	// Kept briefly: long enough to spare the next command a round trip,
	// short enough that a route nobody corrects goes stale on its own.
	// A replica's Position is deliberately NOT cached — it moves every
	// second, and a stale one would send a read to a member that is
	// further behind than the caller asked for.
	if b, err := json.Marshal(cachedTarget{Namespace: t.Namespace, NodeID: t.NodeID, BaseURL: t.BaseURL, Primary: t.Primary, PrimaryControl: t.PrimaryControl}); err == nil {
		c.cachePut(c.routeKey(namespace), b, time.Now().Add(routeCacheFor))
	}

	return t, nil
}

// routeCacheFor is how long a cached route is used. Leadership moves are
// answered by a 307 that drops the entry, so this is only the bound on a
// route nothing corrects — a namespace nobody is writing to.
const routeCacheFor = 5 * time.Minute

// cachedTarget is the part of a Target worth keeping between processes:
// where the group and its leader are. Replica positions are left out on
// purpose (they move constantly).
type cachedTarget struct {
	Namespace      string `json:"namespace"`
	NodeID         string `json:"node_id"`
	BaseURL        string `json:"base_url"`
	Primary        string `json:"primary"`
	PrimaryControl string `json:"primary_control"`
}

func (c *Client) cachedRoute(namespace string) (Target, bool) {
	b, ok := c.cacheGet(c.routeKey(namespace))
	if !ok {
		return Target{}, false
	}

	var ct cachedTarget
	if err := json.Unmarshal(b, &ct); err != nil || ct.BaseURL == "" && ct.Primary == "" {
		c.cacheDrop(c.routeKey(namespace))
		return Target{}, false
	}

	return Target{
		Namespace: ct.Namespace, NodeID: ct.NodeID, BaseURL: ct.BaseURL,
		Primary: ct.Primary, PrimaryControl: ct.PrimaryControl,
	}, true
}

// dropRoute forgets a cached route. A 307 means the leader moved, so the
// route is wrong and the TICKET is not: the ticket is per namespace and
// verb and says nothing about which member serves it.
func (c *Client) dropRoute(namespace string) { c.cacheDrop(c.routeKey(namespace)) }

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

	// End unbounded: pin it to the head as of scan start so the scan
	// terminates under live writes. Head asks the member for zero rows,
	// which is the only way to learn the count without paying for rows:
	// any non-zero limit makes the member's engine load an entire sealed
	// Parquet block into memory to serve the page, and the rows that page carried were
	// discarded here anyway before the scan re-issued the request clamped
	// to the head. The engine's contract is readRows' early return on
	// `offset >= totalRows || limit <= 0` (pkg/engine/read.go), which
	// answers TotalRows before any block is listed or read.
	end := opts.End
	if end <= 0 {
		head, err := c.Head(ctx, memberURL, namespace)
		if err != nil {
			return 0, fmt.Errorf("scan %s: read head: %w", namespace, err)
		}

		end = head
	}

	if end <= cur {
		return 0, nil // empty namespace, or a range that starts at/past the head
	}

	var delivered int64

	for {
		// Clamp the request to the remaining range.
		limit := page
		if cur+limit > end {
			limit = end - cur
		}

		if limit <= 0 {
			return delivered, nil
		}

		var res types.ReadResult
		url := base + "?offset=" + strconv.FormatInt(cur, 10) + "&limit=" + strconv.FormatInt(limit, 10)
		if err := c.getJSONData(ctx, url, namespace, &res); err != nil {
			return delivered, fmt.Errorf("scan %s at %d: %w", namespace, cur, err)
		}

		normalizeNumbers(res.Records)

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

// sendAuthorized sends a directory request with the customer credential
// and gives a 401 exactly ONE refresh-and-retry — the same single retry
// the append path, getJSONData and the index path already give theirs.
//
// Without it, getJSON and doJSON (route lookups, find, every directory
// JSON call) failed hard on an acting token the other paths would simply
// have refreshed: seen 2026-09-15, when two parley sessions on one machine
// both had `parley wait` end on "HTTP 401: authentication required" from
// the route lookup at the same moment, and a read straight afterwards
// succeeded.
//
// build makes a NEW request each time, so a retried POST resends its whole
// body. Retrying a POST is safe here because a 401 is refused at
// authentication, before any handler has done anything.
//
// The retry only happens when there is something to refresh WITH: a
// durable credential the client can exchange. A caller-supplied static
// Bearer cannot be refreshed, so a 401 on one is answered as it stands
// rather than by re-sending the same dead token.
func (c *Client) sendAuthorized(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}

		if err := c.authorize(ctx, req); err != nil {
			return nil, err
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 || !c.canRefreshBearer() {
			return resp, nil
		}

		resp.Body.Close()
		c.dropBearer()
	}
}

// canRefreshBearer answers whether a 401 could be cured by exchanging the
// durable credential again: never for a caller-supplied static Bearer, and
// never when there is no credential configured at all.
func (c *Client) canRefreshBearer() bool {
	return c.Bearer == "" && c.Credentials.Configured()
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
	resp, err := c.sendAuthorized(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Accept", "application/json")
		if c.Token != "" {
			req.Header.Set("X-Cluster-Token", c.Token)
		}

		return req, nil
	})
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
	t, err := c.resolveForCredential(ctx, namespace)
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
				// The cached route named a member that no longer leads.
				a.c.dropRoute(a.namespace)
				a.url = ans.LeaderURL
				continue
			}

			// A redirect that names nobody: leadership is still settling.
			// Back off and ask the directory, as a 503 does — this used to
			// fall through into the 401 case, which only dropped the ticket
			// and retried the same member.
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
			}

			if t, err := a.c.resolveForCredential(ctx, a.namespace); err == nil && t.ReadURL() != "" {
				a.url = t.ReadURL()
			}

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
			// A 503 usually means leadership in motion (handled below), but
			// two budget refusals also answer 503: the block-load budget
			// (2026-09-18 incident; should not arise from an append, which
			// never reads block data) and the write budget (RFC-0022 D-M8,
			// over_write_budget — the one that genuinely CAN arise here).
			// Either code is checked FIRST so it is never mistaken for
			// leadership churn: re-resolving and retrying would be wasted
			// work that also hides the refusal from the caller, who owns
			// whatever policy comes next (see OverBudgetError's doc comment).
			msg, text := readRefusalBody(resp.Body)
			resp.Body.Close()
			plain := fmt.Errorf("append %s: HTTP 503: %s", a.namespace, text)
			if wrapped := wrapIfOverBudget(plain, resp.StatusCode, resp.Header, msg); wrapped != plain {
				return res, wrapped // over_memory_budget/over_write_budget: surface at once, never retried here
			}

			// Leadership in motion: ask the directory again, briefly later.
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
			}

			// Re-resolve on the plane this client holds a credential for
			// (OPS-2, code review 2026-09-21): Resolve is the operator plane
			// and needs the cluster token, so a customer credential got a
			// 401 there, kept the dead member's URL and retried it five
			// times — the self-healing a leaderless member relies on was
			// lost for every customer client. Route is the read-only
			// resolution any credential may make, and is what Appender()
			// itself uses to pick the first URL.
			if t, err := a.c.resolveForCredential(ctx, a.namespace); err == nil && t.ReadURL() != "" {
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

	out, err := c.Redeem(ctx, Redemption{Token: token, PublicKey: publicKey, Alg: alg, Label: label, Caps: caps})
	if err != nil {
		return nil, err
	}

	rec, _ := out["authenticator"].(map[string]any)
	return rec, nil
}

// Invitation is the policy an admin binds to an `in_` token: what kind of
// identity it creates, the capability ceiling for every key it makes, and
// how many times and for how long it may be redeemed
// (docs/cluster/ENROLLMENT.md §7).
type Invitation struct {
	Kind             string            `json:"kind,omitempty"` // service | agent (default agent)
	Caps             []string          `json:"caps,omitempty"` // ceiling; default read,write,own
	NamePrefix       string            `json:"name_prefix,omitempty"`
	Uses             *int              `json:"uses,omitempty"` // 1 (default), n, or 0 = unlimited until expiry
	TTLSeconds       int               `json:"ttl_seconds,omitempty"`
	Ephemeral        bool              `json:"ephemeral,omitempty"`
	EphemeralSeconds int               `json:"ephemeral_seconds,omitempty"`
	Sponsor          string            `json:"sponsor,omitempty"`        // a member of this tenant (default: the minter)
	SponsorAccess    string            `json:"sponsor_access,omitempty"` // read | own (default own)
	Labels           map[string]string `json:"labels,omitempty"`
}

// MintInvitation binds a policy and answers the raw token (shown ONCE)
// with the URL a host redeems it at. Admin seat with `manage`.
func (c *Client) MintInvitation(ctx context.Context, policy Invitation) (token, enrollURL string, invitation map[string]any, err error) {
	var out struct {
		Token      string         `json:"token"`
		EnrollURL  string         `json:"enroll_url"`
		Invitation map[string]any `json:"invitation"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tenant/invitations", policy, &out); err != nil {
		return "", "", nil, err
	}

	return out.Token, out.EnrollURL, out.Invitation, nil
}

// Invitations lists this tenant's invitations (never their tokens) with
// the uses left and the identities each has created.
func (c *Client) Invitations(ctx context.Context) ([]map[string]any, error) {
	var out struct {
		Invitations []map[string]any `json:"invitations"`
	}
	if err := c.getJSON(ctx, c.Directory+"/api/v1/tenant/invitations", &out); err != nil {
		return nil, err
	}

	return out.Invitations, nil
}

// RevokeInvitation ends further redemptions. Identities it already created
// are unaffected.
func (c *Client) RevokeInvitation(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/tenant/invitations/"+id, nil, nil)
}

// ErrPassphraseRequired is what a redemption answers when the token was
// minted with a passphrase (docs/cluster/ENROLLMENT.md §7.3) and none was
// presented. It is the signal to prompt: the token is NOT spent, so a
// second attempt with the phrase succeeds.
var ErrPassphraseRequired = errors.New("client: this token was minted with a passphrase")

// Redemption is one POST /auth/enroll. It covers BOTH token kinds — an
// `en_` enrollment token registering a key on a member named in advance,
// and an `in_` invitation creating a whole new principal — because the
// server tells them apart by prefix and the fields a caller may set are
// the same shape.
type Redemption struct {
	Token      string   // en_… or in_…
	PublicKey  string   // base64 raw public key; the private half stays home
	Alg        string   // "" → ed25519
	Label      string   // which machine or session this key is
	Passphrase string   // the §1a second secret, when the token carries one
	Caps       []string // nil → the token's own; may only narrow
	Username   string   // invitations only: propose a name under the prefix
	Proof      string   // invitations only, MANDATORY: see below
}

// Redeem submits a Redemption and answers the server's record — for an
// `en_` token {authenticator}, for an `in_` invitation {identity,
// membership, authenticator}.
//
// For an invitation, Proof is mandatory and binds the request to this key:
// base64url(ed25519(SHA-256(token) ‖ public key)) —
// `identityfile.File.InvitationProof` builds it. Without it a reusable
// token would let anyone who saw it mint a principal with a key of their
// choosing. Username "" lets the server name the identity
// <prefix>-<key fingerprint>.
//
// A missing passphrase answers ErrPassphraseRequired without spending the
// token, so a caller may prompt and retry.
func (c *Client) Redeem(ctx context.Context, req Redemption) (map[string]any, error) {
	if req.Token == "" || req.PublicKey == "" {
		return nil, fmt.Errorf("client: redeem needs a token and a public key")
	}

	if strings.HasPrefix(req.Token, "in_") && req.Proof == "" {
		return nil, fmt.Errorf("client: an invitation needs a proof of possession")
	}

	body := map[string]any{"token": req.Token, "public_key": req.PublicKey, "label": req.Label}
	for k, v := range map[string]string{"alg": req.Alg, "proof": req.Proof, "username": req.Username, "passphrase": req.Passphrase} {
		if v != "" {
			body[k] = v
		}
	}

	if req.Caps != nil {
		body["caps"] = req.Caps
	}

	var out map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/auth/enroll", body, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// doJSON is the shared write-verb helper (POST/PATCH/DELETE + decode).
func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	if c.Directory == "" {
		return fmt.Errorf("client: no directory URL configured")
	}

	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}

		payload = b
	}

	resp, err := c.sendAuthorized(ctx, func() (*http.Request, error) {
		// A fresh reader per attempt: a retry must send the whole body
		// again, not the remainder of one the first attempt consumed.
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}

		req, err := http.NewRequestWithContext(ctx, method, c.Directory+path, body)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")
		if c.Token != "" {
			req.Header.Set("X-Cluster-Token", c.Token)
		}

		return req, nil
	})
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
		// The server tags refusals a caller can act on with a stable code;
		// `passphrase_required` is the one that means "ask and retry".
		var body struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(msg, &body) == nil && body.Code == "passphrase_required" {
			return fmt.Errorf("%w: %v", ErrPassphraseRequired, err)
		}

		return err
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
