// The index read surface (RFC-0018 §2, §5): lookup, range, vector search,
// and positions-to-rows, all routed through the directory like Scan and
// gated by the same read ticket. Declarations (SetIndexes) go through the
// directory's namespace call instead of a member.
//
// The wire types below (IndexState, SearchHit, IndexDefinition,
// PositionedRecord) mirror pkg/index and pkg/engine field for field but
// are DECLARED HERE ON PURPOSE: importing those packages would drag
// Pebble, cockroachdb/errors and the Prometheus client into every
// consumer of this SDK, and this package's contract is that it depends on
// nothing but the standard library and pkg/types (see client.go's package
// comment). If a field is added on the engine side, add it here too.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/quantumwake/statefs/pkg/ticket"
	"github.com/quantumwake/statefs/pkg/types"
)

// IndexState is one index's state as the member reports it (the same
// record the heartbeat carries): how far it is built, whether it answers,
// and the declaration epoch it was built under. A caller reads
// BuiltThrough to tell an empty answer from an index that is behind.
type IndexState struct {
	State        string `json:"state"`
	BuiltThrough int64  `json:"built_through"`
	Epoch        int    `json:"epoch"`
}

// IndexStateReady is the State value that means the index answers: every
// row at or below BuiltThrough is indexed.
const IndexStateReady = "ready"

// SearchHit is one answer from a vector index: a position and how close
// it is, higher being closer.
type SearchHit struct {
	Position int64   `json:"position"`
	Score    float32 `json:"score"`
}

// IndexDefinition declares one index on a namespace's column. Dimensions,
// Metric and Model are vector-only and stay off the wire when unset.
type IndexDefinition struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Column string `json:"column"`

	Dimensions int    `json:"dimensions,omitempty"`
	Metric     string `json:"metric,omitempty"`
	Model      string `json:"model,omitempty"`
}

// PositionedRecord is one row answered by the rows endpoint: its absolute
// position plus the record itself.
type PositionedRecord struct {
	Position int64        `json:"position"`
	Record   types.Record `json:"record"`
}

// postingsPage is one page of a lookup/range answer (RFC-0018 §2): the
// positions on this page, and the cursor to ask for the next one — the
// last position on the page, or null when the page came back empty.
type postingsPage struct {
	Positions []int64 `json:"positions"`
	Next      *int64  `json:"next"`
}

// Indexes answers every declared index's state for a namespace, keyed by
// index name. An empty map means the namespace declares no indexes.
func (c *Client) Indexes(ctx context.Context, namespace string) (map[string]IndexState, error) {
	base, err := c.memberBase(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var out map[string]IndexState
	if err := c.getJSONData(ctx, base+"/api/v1/index/"+url.PathEscape(namespace), namespace, &out); err != nil {
		return nil, fmt.Errorf("client: indexes %s: %w", namespace, err)
	}

	return out, nil
}

// Lookup answers every position whose indexed column carries key, walking
// every page (RFC-0018 §2's `next` cursor) so the caller sees the whole
// posting list in one call.
func (c *Client) Lookup(ctx context.Context, namespace, indexName string, key any) ([]int64, error) {
	text, kind, err := encodeKind(key)
	if err != nil {
		return nil, fmt.Errorf("client: lookup %s.%s: %w", namespace, indexName, err)
	}

	return c.walkPostings(ctx, namespace, indexName, "postings", url.Values{"key": {text}, "kind": {kind}})
}

// Range answers every position whose indexed column falls in [low, high],
// walking every page. Either bound may be nil for an open-ended range;
// both nil is refused here, because that asks the member for nothing in
// particular. When both are given they must carry the same kind — they
// name one ordered range, not two independent keys.
//
// An empty-string bound cannot be distinguished from an absent one on the
// query line, so it reads as open: range a string column from "" only if
// you mean "from the beginning".
func (c *Client) Range(ctx context.Context, namespace, indexName string, low, high any) ([]int64, error) {
	params, err := rangeParams(low, high)
	if err != nil {
		return nil, fmt.Errorf("client: range %s.%s: %w", namespace, indexName, err)
	}

	return c.walkPostings(ctx, namespace, indexName, "range", params)
}

// rangeParams renders the bounds onto the query line: the bounds that are
// given, plus the one kind they share. An absent bound contributes no
// parameter at all, which is how the member reads an open bound.
func rangeParams(low, high any) (url.Values, error) {
	lowText, lowKind, err := encodeBound(low)
	if err != nil {
		return nil, fmt.Errorf("low: %w", err)
	}

	highText, highKind, err := encodeBound(high)
	if err != nil {
		return nil, fmt.Errorf("high: %w", err)
	}

	if lowKind != "" && highKind != "" && lowKind != highKind {
		return nil, fmt.Errorf("low is %s but high is %s", lowKind, highKind)
	}

	kind := lowKind
	if kind == "" {
		kind = highKind
	}

	if kind == "" {
		return nil, fmt.Errorf("needs at least one of low, high")
	}

	params := url.Values{"kind": {kind}}
	if lowKind != "" {
		params.Set("low", lowText)
	}

	if highKind != "" {
		params.Set("high", highText)
	}

	return params, nil
}

// Search answers the k nearest positions to vector in a vector index,
// closest first. model must match the index's declared model; filter,
// when non-empty, restricts the search to those positions (RFC-0018 §2's
// `filter: {"positions": [...]}`) — typically the answer of a prior
// Lookup or Range. A k that is not positive, a wrong model, or a wrong
// dimension count is the member's refusal (HTTP 400), not a local one.
func (c *Client) Search(
	ctx context.Context,
	namespace, indexName, model string,
	// --
	vector []float32, k int, filter []int64) ([]SearchHit, error) {
	base, err := c.memberBase(ctx, namespace)
	if err != nil {
		return nil, err
	}

	endpoint := base + "/api/v1/index/" + url.PathEscape(namespace) + "/" + url.PathEscape(indexName) + "/search"

	var out struct {
		Hits []SearchHit `json:"hits"`
	}
	if err := c.postJSONData(ctx, endpoint, namespace, searchBody(model, vector, k, filter), &out); err != nil {
		return nil, fmt.Errorf("client: search %s.%s: %w", namespace, indexName, err)
	}

	return out.Hits, nil
}

// searchBody builds the search request. vector and k always travel (the
// member needs them even to refuse); model and filter only when set, so
// a single-model index and an unfiltered search send the smaller body.
func searchBody(model string, vector []float32, k int, filter []int64) map[string]any {
	body := map[string]any{"vector": vector, "k": k}
	if model != "" {
		body["model"] = model
	}

	if len(filter) > 0 {
		body["filter"] = map[string]any{"positions": filter}
	}

	return body
}

// rowsPerRequest bounds how many positions ride on one rows URL. The
// positions travel on the query line and a posting list is routinely far
// longer than any server or proxy accepts there (the member pages
// postings at 10,000), so a long ask is split into this many at a time
// and the answers are concatenated. Order survives: each request answers
// in the order it asked, and the requests run in order.
const rowsPerRequest = 256

// Rows turns positions into records, in the order asked, reading through
// the owning member's positional read (blocks + tail). A position past
// the namespace's head is simply omitted from the answer, so the result
// may be shorter than the ask; a repeated position answers once per
// occurrence, in place.
func (c *Client) Rows(ctx context.Context, namespace string, positions []int64) ([]PositionedRecord, error) {
	if len(positions) == 0 {
		return nil, nil
	}

	base, err := c.memberBase(ctx, namespace)
	if err != nil {
		return nil, err
	}

	var rows []PositionedRecord
	for start := 0; start < len(positions); start += rowsPerRequest {
		batch := positions[start:min(start+rowsPerRequest, len(positions))]

		answered, err := c.fetchRows(ctx, base, namespace, batch)
		if err != nil {
			return nil, fmt.Errorf("client: rows %s: batch at offset %d of %d asked: %w", namespace, start, len(positions), err)
		}

		rows = append(rows, answered...)
	}

	return rows, nil
}

// fetchRows asks one batch of positions and normalizes the records it
// answers. getJSONData decodes with UseNumber, so every JSON number
// arrives as a json.Number; normalizeNumbers turns it back into an exact
// int64 (or a float64), without which a sequence number past 2^53 would
// silently lose its low digits — the same treatment Scan gives a page.
// The records it normalizes are the very maps out.Rows holds (a
// types.Record is a map), so the fix is visible through the answer.
func (c *Client) fetchRows(ctx context.Context, base, namespace string, positions []int64) ([]PositionedRecord, error) {
	parts := make([]string, len(positions))
	for i, position := range positions {
		parts[i] = strconv.FormatInt(position, 10)
	}

	endpoint := base + "/api/v1/state/" + url.PathEscape(namespace) + "/rows?positions=" + strings.Join(parts, ",")

	var out struct {
		Rows []PositionedRecord `json:"rows"`
	}
	if err := c.getJSONData(ctx, endpoint, namespace, &out); err != nil {
		return nil, err
	}

	records := make([]types.Record, len(out.Rows))
	for i := range out.Rows {
		records[i] = out.Rows[i].Record
	}

	normalizeNumbers(records)
	return out.Rows, nil
}

// SetIndexes declares a namespace's indexes through the directory
// (RFC-0018 §3), which validates them, fans the declaration to every
// member of the namespace's group, and reports ready once every member's
// heartbeat agrees at the current epoch.
//
// The declaration is REPLACED wholesale: an index the list omits is
// dropped, and an empty list drops every index. Permission is the
// namespace lifecycle rule that durability and cordon use — the owning
// bearer, not merely a writer.
func (c *Client) SetIndexes(ctx context.Context, namespace string, definitions []IndexDefinition) error {
	return c.doJSON(ctx, http.MethodPatch, "/api/v1/cluster/namespaces/"+namespace+"/indexes",
		map[string]any{"indexes": definitions}, nil)
}

// memberBase routes a namespace and answers the member URL every index
// call prefixes, without its trailing slash. A group with no reachable
// member is refused here rather than left to build a relative request URL
// that fails obscurely inside net/http.
func (c *Client) memberBase(ctx context.Context, namespace string) (string, error) {
	target, err := c.Route(ctx, namespace)
	if err != nil {
		return "", fmt.Errorf("client: route %s: %w", namespace, err)
	}

	readURL := target.ReadURL()
	if c.InCluster {
		readURL = target.InClusterReadURL()
	}

	base := strings.TrimRight(readURL, "/")
	if base == "" {
		return "", fmt.Errorf("client: %s: group %s has no reachable member", namespace, target.NodeID)
	}

	return base, nil
}

// walkPostings drives one paged posting endpoint (postings or range) to
// exhaustion, appending each page's positions in order.
//
// Two things end the walk, and both are needed. A null `next` ends it,
// but the member answers a non-null cursor on EVERY non-empty page,
// including the last one, so a walk that stopped only on null would ask
// one more time and get an empty page — which is the second stop. A
// cursor that fails to advance is a server fault and is refused rather
// than followed: following it loops forever.
func (c *Client) walkPostings(
	ctx context.Context,
	namespace, indexName, verb string,
	// --
	params url.Values) ([]int64, error) {
	base, err := c.memberBase(ctx, namespace)
	if err != nil {
		return nil, err
	}

	endpoint := base + "/api/v1/index/" + url.PathEscape(namespace) + "/" + url.PathEscape(indexName) + "/" + verb

	var positions []int64
	var cursor *int64

	for {
		page, err := c.fetchPostingsPage(ctx, endpoint, namespace, params, cursor)
		if err != nil {
			return nil, fmt.Errorf("client: %s %s.%s: %w", verb, namespace, indexName, err)
		}

		positions = append(positions, page.Positions...)
		if page.Next == nil || len(page.Positions) == 0 {
			return positions, nil
		}

		if cursor != nil && *page.Next <= *cursor {
			return nil, fmt.Errorf("client: %s %s.%s: paging cursor did not advance past %d", verb, namespace, indexName, *cursor)
		}

		cursor = page.Next
	}
}

// fetchPostingsPage asks for one page: the caller's fixed parameters plus
// the paging cursor, which is absent on the first page.
func (c *Client) fetchPostingsPage(
	ctx context.Context,
	endpoint, namespace string,
	params url.Values,
	// --
	cursor *int64) (postingsPage, error) {
	query := cloneValues(params)
	if cursor != nil {
		query.Set("next", strconv.FormatInt(*cursor, 10))
	}

	var page postingsPage
	if err := c.getJSONData(ctx, endpoint+"?"+query.Encode(), namespace, &page); err != nil {
		return postingsPage{}, err
	}

	return page, nil
}

// cloneValues copies a url.Values so callers can add per-page parameters
// (the paging cursor) without mutating the caller's original set.
func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}

	return out
}

// encodeBound is encodeKind for a range bound that may be absent: a nil
// bound answers empty text and an empty kind, which Range reads as open.
func encodeBound(v any) (text, kind string, err error) {
	if v == nil {
		return "", "", nil
	}

	return encodeKind(v)
}

// encodeKind renders a lookup or range key as the query-line text plus
// the `kind` that says how to read it back (RFC-0018 §2): a string is
// `string`, a bool is `bool`, and any integer or float is `number`. bool
// is matched before the numeric kinds because a reader coming from a
// dynamic language would ask (in Python a bool IS an int).
//
// The reflect pass exists for NAMED types — a `type UserID int64` or a
// `type Tag string` is what a caller's own record fields usually are, and
// a plain type switch would refuse them. A float32 is formatted at 32-bit
// precision, or float32(0.1) would go out as 0.10000000149011612.
func encodeKind(v any) (text, kind string, err error) {
	switch t := v.(type) {
	case string:
		return t, "string", nil
	case bool:
		return strconv.FormatBool(t), "bool", nil
	}

	rv := reflect.ValueOf(v) // an invalid Value for nil: falls to the refusal
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), "string", nil
	case reflect.Bool:
		return strconv.FormatBool(rv.Bool()), "bool", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), "number", nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), "number", nil
	case reflect.Float32:
		return strconv.FormatFloat(rv.Float(), 'g', -1, 32), "number", nil
	case reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'g', -1, 64), "number", nil
	default:
		return "", "", fmt.Errorf("unsupported key type %T", v)
	}
}

// postJSONData is the member data-plane POST counterpart to getJSONData,
// signed with the READ verb because the one POST on this surface — the
// vector search — is a query: a read-only grant must admit it. A POST
// that actually writes must NOT go through here.
func (c *Client) postJSONData(ctx context.Context, requestURL, namespace string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode body for %s: %w", requestURL, err)
	}

	// One 401 earns a refresh-and-retry (a grant can be revoked mid-TTL);
	// a second is an honest authorization failure and surfaces below.
	for attempt := 0; ; attempt++ {
		resp, err := c.postSignedRead(ctx, requestURL, namespace, body)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && c.hasCustomerCredential() {
			resp.Body.Close()
			c.dropTicket(namespace)
			c.dropBearer()
			continue
		}

		return decodeDataAnswer(resp, requestURL, out)
	}
}

// postSignedRead builds and sends one ticket-signed POST. The ticket MAC
// binds these exact bytes, so the body is marshalled once by the caller
// and replayed on the retry.
func (c *Client) postSignedRead(ctx context.Context, requestURL, namespace string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build POST %s: %w", requestURL, err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if err := c.signDataPlane(ctx, req, namespace, ticket.VerbRead, body); err != nil {
		return nil, fmt.Errorf("sign POST %s: %w", requestURL, err)
	}

	return c.HTTP.Do(req)
}

// decodeDataAnswer closes resp and turns it into out, or into the error
// the status names. The message keeps the "HTTP <code>" shape the rest of
// this package uses, so IsConflict (409: the index is not ready),
// IsNotFound (404: no such index) and IsRefused (401/403) recognize a
// member's refusal here exactly as they do a directory's.
func decodeDataAnswer(resp *http.Response, requestURL string, out any) error {
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s answered HTTP %d: %s", requestURL, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if out == nil {
		return nil
	}

	dec := json.NewDecoder(resp.Body)
	dec.UseNumber() // exact int64s — float64 corrupts values past 2^53
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", requestURL, err)
	}

	return nil
}
