// Package spool is the S3 seam: the plugin's local, append-only buffer
// between capture and delivery. One JSONL file per session holds events
// in arrival order; a sidecar holds the byte offset of the last line the
// push acknowledged. Several processes append to one session (each hook
// is its own process, the tailer is another), which O_APPEND single-write
// lines make safe; exactly one push process reads and acks.
package spool

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/quantumwake/parley/pkg/event"
)

// Session names one spool.
type Session struct {
	Dir string // spool directory
	ID  string // client session id
}

// Path is the JSONL file; AckPath the offset sidecar.
func (s Session) Path() string    { return filepath.Join(s.Dir, safe(s.ID)+".jsonl") }
func (s Session) AckPath() string { return filepath.Join(s.Dir, safe(s.ID)+".ack") }

// Append validates and writes one event as one line, fsyncing when sync
// is set (the plugin sets it for session.end).
func (s Session) Append(e event.Event, sync bool) error {
	if err := e.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}

	if sync {
		return f.Sync()
	}

	return nil
}

// Entry is one spooled event and the offset just past its line.
type Entry struct {
	Event event.Event
	Index int64 // zero-based line number; the push uses it as seq
	Next  int64 // byte offset after this line; pass to Ack
}

// Read yields entries starting at byte offset from (0 = the beginning). A
// trailing partial line (a writer mid-append) ends the iteration without
// error; the next Read picks it up once complete.
func (s Session) Read(from int64) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		f, err := os.Open(s.Path())
		if errors.Is(err, os.ErrNotExist) {
			return
		}

		if err != nil {
			yield(Entry{}, err)
			return
		}

		defer f.Close()
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			yield(Entry{}, err)
			return
		}

		idx := lineIndex(s.Path(), from)
		r := bufio.NewReaderSize(f, 1<<20)
		off := from
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return // EOF or a partial trailing line: stop here
			}

			off += int64(len(line))
			var e event.Event
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				if !yield(Entry{}, fmt.Errorf("spool: %s at %d: %w", s.Path(), off, jerr)) {
					return
				}

				idx++
				continue
			}

			if !yield(Entry{Event: e, Index: idx, Next: off}, nil) {
				return
			}

			idx++
		}
	}
}

// Ack records that everything before offset has been delivered.
func (s Session) Ack(offset int64) error {
	tmp := s.AckPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, s.AckPath())
}

// AckOffset is the last acknowledged offset (0 when none).
func (s Session) AckOffset() int64 {
	b, err := os.ReadFile(s.AckPath())
	if err != nil {
		return 0
	}

	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// lineIndex counts complete lines before byte offset from, so Index is
// stable across reads that start mid-file.
func lineIndex(path string, from int64) int64 {
	if from == 0 {
		return 0
	}

	f, err := os.Open(path)
	if err != nil {
		return 0
	}

	defer f.Close()
	var n int64
	r := bufio.NewReader(io.LimitReader(f, from))
	for {
		_, err := r.ReadBytes('\n')
		if err != nil {
			return n
		}

		n++
	}
}

func safe(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}

		return '_'
	}, id)
}
