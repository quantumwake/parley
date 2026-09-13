package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/quantumwake/statefs.ai/pkg/event"
)

// File is a Store on the local filesystem: an index of namespaces and one
// JSONL file of rows per namespace. It exists for offline runs and spikes
// (capture without a cluster) and passes the same conformance test as the
// Fake and the statefs adapter. It is not a durability story.
type File struct {
	dir string
	mu  sync.Mutex
	idx fileIndex
}

type fileIndex struct {
	Next       int                  `json:"next"`
	ByName     map[string]string    `json:"by_name"`
	Namespaces map[string]Namespace `json:"namespaces"`
	Order      []string             `json:"order"`
}

// NewFile opens or creates a file store under dir.
func NewFile(dir string) (*File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	f := &File{dir: dir, idx: fileIndex{ByName: map[string]string{}, Namespaces: map[string]Namespace{}}}
	b, err := os.ReadFile(f.indexPath())
	if err == nil {
		if err := json.Unmarshal(b, &f.idx); err != nil {
			return nil, err
		}
	}

	return f, nil
}

func (f *File) indexPath() string         { return filepath.Join(f.dir, "namespaces.json") }
func (f *File) rowsPath(id string) string { return filepath.Join(f.dir, id+".jsonl") }

func (f *File) saveLocked() error {
	b, err := json.MarshalIndent(f.idx, "", "  ")
	if err != nil {
		return err
	}

	tmp := f.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, f.indexPath())
}

// Open implements Store.
func (f *File) Open(_ context.Context, displayName string, scope Scope) (Namespace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.idx.ByName[displayName]; ok {
		return f.withHeadLocked(f.idx.Namespaces[id]), nil
	}

	f.idx.Next++
	id := "file-" + strconv.Itoa(f.idx.Next)
	cp := Scope{}
	for k, v := range scope {
		cp[k] = v
	}

	ns := Namespace{ID: id, DisplayName: displayName, Scope: cp}
	f.idx.ByName[displayName] = id
	f.idx.Namespaces[id] = ns
	f.idx.Order = append(f.idx.Order, id)
	if err := f.saveLocked(); err != nil {
		return Namespace{}, err
	}

	return ns, nil
}

// Append implements Store.
func (f *File) Append(_ context.Context, ns string, events []event.Event, _ bool) (Position, error) {
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return 0, joinErr(ErrInvalidEvent, err)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.idx.Namespaces[ns]; !ok {
		return 0, ErrNotFound
	}

	start := f.countLocked(ns)
	fh, err := os.OpenFile(f.rowsPath(ns), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}

	defer fh.Close()
	w := bufio.NewWriter(fh)
	for _, e := range events {
		line, _ := json.Marshal(e)
		if _, err := w.Write(append(line, '\n')); err != nil {
			return 0, err
		}
	}

	if err := w.Flush(); err != nil {
		return 0, err
	}

	return start, nil
}

// Scan implements Store.
func (f *File) Scan(_ context.Context, ns string, from, to Position) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		f.mu.Lock()
		if _, ok := f.idx.Namespaces[ns]; !ok {
			f.mu.Unlock()
			yield(event.Event{}, ErrNotFound)
			return
		}

		rows, err := f.readAllLocked(ns)
		f.mu.Unlock()
		if err != nil {
			yield(event.Event{}, err)
			return
		}

		head := Position(len(rows))
		if to <= 0 || to > head {
			to = head
		}

		if from < 0 {
			from = 0
		}

		for i := from; i < to; i++ {
			if !yield(rows[i], nil) {
				return
			}
		}
	}
}

// Head implements Store.
func (f *File) Head(_ context.Context, ns string) (Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.idx.Namespaces[ns]; !ok {
		return 0, ErrNotFound
	}

	return f.countLocked(ns), nil
}

// Find implements Store.
func (f *File) Find(_ context.Context, filter Scope, limit int) ([]Namespace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := append([]string(nil), f.idx.Order...)
	sort.Slice(ids, func(i, j int) bool { return numOf(ids[i]) > numOf(ids[j]) })
	var out []Namespace
	for _, id := range ids {
		ns := f.idx.Namespaces[id]
		if !ns.Scope.Contains(filter) {
			continue
		}

		out = append(out, f.withHeadLocked(ns))
		if limit > 0 && len(out) >= limit {
			break
		}
	}

	return out, nil
}

func (f *File) withHeadLocked(ns Namespace) Namespace {
	ns.Head = f.countLocked(ns.ID)
	return ns
}

func (f *File) countLocked(id string) Position {
	fh, err := os.Open(f.rowsPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}

	if err != nil {
		return 0
	}

	defer fh.Close()
	var n Position
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		n++
	}

	return n
}

func (f *File) readAllLocked(id string) ([]event.Event, error) {
	fh, err := os.Open(f.rowsPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	defer fh.Close()
	var out []event.Event
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var e event.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, err
		}

		out = append(out, e)
	}

	return out, sc.Err()
}

func numOf(id string) int {
	n, _ := strconv.Atoi(id[len("file-"):])
	return n
}

// Describe implements Store.
func (f *File) Describe(_ context.Context, ns string, labels Scope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.idx.Namespaces[ns]
	if !ok {
		return ErrNotFound
	}

	for k, v := range labels {
		n.Scope[k] = v
	}

	f.idx.Namespaces[ns] = n
	return f.saveLocked()
}
