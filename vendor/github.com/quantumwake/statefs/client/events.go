// events.go — the client side of the live tail (RFC-0025).
//
// Scan pages a range and ends at the head. Events does not end at the head:
// it delivers rows from a position and keeps delivering as they are
// appended, so a caller that wants to know NOW stops asking every couple of
// seconds. Everything else is the same read — same ticket, same verb, same
// refusals — and a member that does not serve the route answers 404, which
// is ErrEventsUnsupported here so a caller can fall back to Scan.
//
// Reconnection is deliberately the CALLER's, exactly as re-resolution after
// a member moves already is: a stream ends on its own every few minutes,
// when the ticket that opened it expires, and only the caller knows whether
// it still wants to be listening.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/quantumwake/statefs/pkg/ticket"
	"github.com/quantumwake/statefs/pkg/types"
)

// ErrEventsUnsupported: this member does not serve a live tail (the route
// is off, or it is older than RFC-0025). Page instead.
var ErrEventsUnsupported = errors.New("client: this member does not serve a live tail")

// EventsOptions bound a live tail.
type EventsOptions struct {
	// From is the first position to deliver (0 = the beginning). After a
	// stream ends, pass the position Events returned.
	From int64
	// Batch is the most rows one event may carry; the member clamps it to
	// its own read cap. 0 = the member's default.
	Batch int64
}

// Events follows a namespace from opts.From, calling fn with each batch of
// rows AS THEY LAND, and returns the position it reached when the stream
// ends. A stream ends cleanly (nil error) when the ticket that opened it
// expires or the member closes it; the caller decides whether to open
// another from the returned position.
//
//	memberURL the member to tail (Target.ReadURLFor after Route, or a
//	          direct member URL).
//	namespace the log to follow.
//	opts      where to start and how much per event.
//	fn        the per-batch consumer; an error from it ends the stream.
func (c *Client) Events(
	ctx context.Context,
	memberURL, namespace string,
	opts EventsOptions,
	// --
	fn func(offset int64, records []types.Record) error) (int64, error) {
	from := opts.From
	if from < 0 {
		from = 0
	}

	url := strings.TrimRight(memberURL, "/") + "/api/v1/state/" + namespace + "/events?from=" + strconv.FormatInt(from, 10)
	if opts.Batch > 0 {
		url += "&batch=" + strconv.FormatInt(opts.Batch, 10)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return from, err
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if err := c.signDataPlane(ctx, req, namespace, ticket.VerbRead, nil); err != nil {
		return from, err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return from, err
	}

	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return from, ErrEventsUnsupported
	}

	if resp.StatusCode != http.StatusOK {
		msg, text := readRefusalBody(resp.Body)
		err := fmt.Errorf("%s answered HTTP %d: %s", url, resp.StatusCode, text)
		return from, wrapIfOverBudget(err, resp.StatusCode, resp.Header, msg)
	}

	return consume(bufio.NewReader(resp.Body), from, fn)
}

// consume reads the stream's frames until it ends, answering the position
// reached. A frame the client does not know is skipped, so the member may
// add one without breaking an older client.
func consume(body *bufio.Reader, from int64, fn func(int64, []types.Record) error) (int64, error) {
	var id, event string
	var data []byte

	for {
		line, err := body.ReadString('\n')
		if line == "" && err != nil {
			// The member closed the stream, or the context was cancelled:
			// either way this is where the caller decides what next.
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return from, nil
			}

			return from, err
		}

		switch line = strings.TrimRight(line, "\r\n"); {
		case line == "":
			next, done, ferr := deliver(id, event, data, from, fn)
			if ferr != nil {
				return next, ferr
			}

			from, id, event, data = next, "", "", nil
			if done {
				return from, nil
			}
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:"))...)
		}

		if err != nil {
			return from, nil // a final frame with no trailing newline
		}
	}
}

// deliver acts on one complete frame: rows go to fn, head is liveness,
// expired ends the stream cleanly, error ends it with the member's own
// classification.
func deliver(id, event string, data []byte, from int64, fn func(int64, []types.Record) error) (int64, bool, error) {
	switch event {
	case "rows":
		var payload struct {
			Offset  int64          `json:"offset"`
			Records []types.Record `json:"records"`
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber() // exact int64s — float64 corrupts values past 2^53
		if err := dec.Decode(&payload); err != nil {
			return from, false, fmt.Errorf("live tail: unreadable rows event: %w", err)
		}

		normalizeNumbers(payload.Records)
		if err := fn(payload.Offset, payload.Records); err != nil {
			return from, false, err
		}

		// The id is where a reconnect resumes; fall back to counting the
		// rows if a member ever sends one without.
		if next, err := strconv.ParseInt(id, 10, 64); err == nil {
			return next, false, nil
		}

		return payload.Offset + int64(len(payload.Records)), false, nil

	case "expired", "moved":
		// Not errors: the stream ran out of authority, or the namespace is
		// served elsewhere now. The caller re-opens (re-resolving first,
		// for moved) from the position it reached.
		return from, true, nil

	case "error":
		var refusal struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &refusal)
		if refusal.Code == "" {
			return from, true, fmt.Errorf("live tail: the member ended the stream")
		}

		return from, true, fmt.Errorf("live tail: %s (%s)", refusal.Message, refusal.Code)
	}

	return from, false, nil // head, a comment, or an event this client does not know
}
