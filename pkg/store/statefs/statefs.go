// Package statefs is the production Store: the enrolled-directory Go client
// underneath, the product's port on top. Credentials, acting tokens and
// per-verb grant tickets are the client's business (S12); this adapter
// never sees a durable secret and never talks to a member without a
// route from the directory.
package statefs

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strings"

	sfs "github.com/quantumwake/statefs/client"
	"github.com/quantumwake/statefs/pkg/types"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

// Config assembles an adapter. Directory and Credentials are required for
// customer flows; InCluster selects pod-DNS member URLs for callers that
// run inside the statefs cluster (the job runner), external ingress hosts
// otherwise (plugins, the console).
type Config struct {
	Directory   string          // directory base URL from enrollment
	Credentials sfs.Credentials // the durable authenticator; zero = sfs.CredentialsFromEnv()
	InCluster   bool            // prefer in-cluster member URLs
	ScanPage    int64           // rows per scan request; <= 0 = 4096
}

// Store implements store.Store against the enrolled directory.
type Store struct {
	c   *sfs.Client
	cfg Config
}

// New builds the adapter; it makes no network call.
func New(cfg Config) *Store {
	if !cfg.Credentials.Configured() {
		cfg.Credentials = sfs.CredentialsFromEnv()
	}

	c := sfs.New(cfg.Directory, "", nil)
	c.Credentials = cfg.Credentials
	return &Store{c: c, cfg: cfg}
}

// Client exposes the underlying statefs client for callers that need a
// statefs-only verb (grants, cordon, delete); product code should not.
func (s *Store) Client() *sfs.Client { return s.c }

// Open implements store.Store: create with the display name, and on a
// duplicate-name refusal return the existing namespace unchanged.
func (s *Store) Open(ctx context.Context, displayName string, scope store.Scope) (store.Namespace, error) {
	made, err := s.c.CreateNamespaceWith(ctx, sfs.CreateNamespaceOpts{DisplayName: displayName, Scope: map[string]any(scope)})
	if err == nil {
		return store.Namespace{ID: made.Namespace, DisplayName: made.DisplayName, Scope: store.Scope(made.Scope), Head: 0}, nil
	}

	if !sfs.IsConflict(err) {
		return store.Namespace{}, mapErr(err)
	}

	// The name is taken: find it by containment on the scope we asked for
	// and match the display name here. The directory's name search needs
	// the manage capability (handoff delta 7), which a plugin key lacks.
	found, ferr := s.FindByName(ctx, displayName, nil) // kind-agnostic: the existing namespace wins whatever scope was asked
	if ferr != nil {
		return store.Namespace{}, ferr
	}

	if found == nil {
		return store.Namespace{}, fmt.Errorf("%w: display name %q exists but is not visible to this identity", store.ErrRefused, displayName)
	}

	return *found, nil
}

// FindByName looks a namespace up by display name through scope
// containment (hint narrows the candidates; nil means kind-agnostic).
func (s *Store) FindByName(ctx context.Context, displayName string, hint store.Scope) (*store.Namespace, error) {
	filter := store.Scope{}
	if k, ok := hint["kind"]; ok {
		filter["kind"] = k
	}

	metas, err := s.c.FindNamespaces(ctx, map[string]any(filter), 1000)
	if err != nil {
		return nil, mapErr(err)
	}

	for _, m := range metas {
		if strings.EqualFold(m.DisplayName, displayName) {
			ns := store.Namespace{ID: m.Namespace, DisplayName: m.DisplayName, Scope: store.Scope(m.Scope), Owner: m.OwnerMembershipID, Head: store.HeadUnknown}
			return &ns, nil
		}
	}

	return nil, nil
}

// Append implements store.Store. Validation runs before any network call
// so a bad batch never partially lands.
func (s *Store) Append(ctx context.Context, ns string, events []event.Event, sync bool) (store.Position, error) {
	records := make([]types.Record, 0, len(events))
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return 0, fmt.Errorf("%w: %v", store.ErrInvalidEvent, err)
		}

		rec, err := e.Record()
		if err != nil {
			return 0, fmt.Errorf("%w: %v", store.ErrInvalidEvent, err)
		}

		records = append(records, types.Record(rec))
	}

	if len(records) == 0 {
		head, err := s.Head(ctx, ns)
		return head, err
	}

	var opts []sfs.AppenderOption
	if sync {
		opts = append(opts, sfs.WithSync())
	}

	ap, err := s.c.Appender(ctx, ns, opts...)
	if err != nil {
		return 0, mapErr(err)
	}

	res, err := ap.Append(ctx, records)
	if err != nil {
		if strings.Contains(err.Error(), sfs.ErrDurabilityNotConfirmed.Error()) {
			return store.Position(res.RowStart), store.ErrDurabilityNotConfirmed
		}

		return 0, mapErr(err)
	}

	return store.Position(res.RowStart), nil
}

// Scan implements store.Store over the client's paged scan.
func (s *Store) Scan(ctx context.Context, ns string, from, to store.Position) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		member, err := s.readURL(ctx, ns)
		if err != nil {
			yield(event.Event{}, err)
			return
		}

		stop := false
		_, err = s.c.Scan(ctx, member, ns, sfs.ScanOptions{Start: int64(from), End: int64(to), Page: s.cfg.ScanPage},
			func(_ int64, records []types.Record) error {
				for _, r := range records {
					e, err := event.FromRecord(map[string]any(r))
					if !yield(e, err) {
						stop = true
						return errStopScan
					}
				}

				return nil
			})
		if err != nil && !stop {
			yield(event.Event{}, mapErr(err))
		}
	}
}

// Head implements store.Store.
func (s *Store) Head(ctx context.Context, ns string) (store.Position, error) {
	member, err := s.readURL(ctx, ns)
	if err != nil {
		return 0, err
	}

	n, err := s.c.Head(ctx, member, ns)
	if err != nil {
		return 0, mapErr(err)
	}

	return store.Position(n), nil
}

// Find implements store.Store: directory containment search, newest pin first.
func (s *Store) Find(ctx context.Context, filter store.Scope, limit int) ([]store.Namespace, error) {
	metas, err := s.c.FindNamespaces(ctx, map[string]any(filter), limit)
	if err != nil {
		return nil, mapErr(err)
	}

	sort.Slice(metas, func(i, j int) bool { return metas[i].PinnedAt.After(metas[j].PinnedAt) })
	out := make([]store.Namespace, 0, len(metas))
	for _, m := range metas {
		out = append(out, store.Namespace{ID: m.Namespace, DisplayName: m.DisplayName, Scope: store.Scope(m.Scope), Owner: m.OwnerMembershipID, Head: store.HeadUnknown})
	}

	return out, nil
}

func (s *Store) readURL(ctx context.Context, ns string) (string, error) {
	t, err := s.c.Route(ctx, ns)
	if err != nil {
		return "", mapErr(err)
	}

	if s.cfg.InCluster {
		return t.InClusterReadURL(), nil
	}

	return t.ReadURL(), nil
}

var errStopScan = fmt.Errorf("scan stopped by consumer")

// mapErr folds the client's HTTP-shaped errors into the port's typed ones,
// keeping the original text for logs.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case sfs.IsNotFound(err):
		return fmt.Errorf("%w: %v", store.ErrNotFound, err)
	case strings.Contains(err.Error(), "HTTP 413"), strings.Contains(err.Error(), "HTTP 507"):
		return fmt.Errorf("%w: %v", store.ErrTooLarge, err)
	case sfs.IsUnauthenticated(err):
		return fmt.Errorf("%w: %v", store.ErrUnauthenticated, err)
	case sfs.IsRefused(err):
		return fmt.Errorf("%w: %v", store.ErrRefused, err)
	}

	return err
}

// Describe implements store.Store through the directory's scope merge.
func (s *Store) Describe(ctx context.Context, ns string, labels store.Scope) error {
	_, err := s.c.EnrichScope(ctx, ns, map[string]any(labels))
	return mapErr(err)
}
