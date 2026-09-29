package statefs

// heartbeat.go — asking a member "are you there" once its reads have gone
// quiet.
//
// The reads are the heartbeat in the normal case: the poller reads every
// followed conversation every two seconds, so every member host answers
// something that often and nothing here sends a byte. What this adds is
// the case where those reads stop answering — a hung connection, a
// balancer that dropped the pool — and it adds two things: the dead pool
// is dropped without waiting for the next read to time out on it, and
// `parley status` can say `reconnecting <host>` instead of looking like a
// quiet channel.

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"sync"
	"time"
)

// memberForget is how long a member stays on the list after its last
// read. A conversation this machine stopped following is not kept alive
// by beats to a member nothing reads any more.
const memberForget = time.Minute

// member is one host the store reads from.
type member struct {
	host  string
	base  string    // the member's URL, what a Head read needs
	ns    string    // a namespace it serves: the cheapest thing to ask it for
	asked time.Time // last read sent to it, whatever came back
	heard time.Time // last read it answered, or beat it answered
	beat  time.Time // last beat sent to it
}

// members is every host the store has read from lately.
type members struct {
	mu   sync.Mutex
	seen map[string]*member
}

// note records one read of ns at base and whether it answered. A refusal
// is an answer: the host is there.
func (m *members) note(base, ns string, err error) {
	host := hostOf(base)
	if host == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]*member{}
	}

	now := time.Now()
	mb := m.seen[host]
	if mb == nil {
		mb = &member{host: host}
		m.seen[host] = mb
	}

	mb.base, mb.ns, mb.asked = base, ns, now
	if err == nil || !unanswered(err) {
		mb.heard = now
	}
}

// answered records a beat that came back.
func (m *members) answered(host string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mb := m.seen[host]; mb != nil {
		mb.heard = time.Now()
	}
}

// due answers the hosts read from within memberForget, split into those
// heard from within every and those that need a beat. A host beaten
// within every is not beaten again yet, whatever it answered.
func (m *members) due(every time.Duration) (heard []string, beat []member) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for host, mb := range m.seen {
		switch {
		case now.Sub(mb.asked) > memberForget:
			delete(m.seen, host)
		case now.Sub(mb.heard) < every:
			heard = append(heard, host)
		case now.Sub(mb.beat) < every:
			// Beaten and missed, and not due again: still a miss.
		default:
			mb.beat = now
			beat = append(beat, *mb)
		}
	}

	return heard, beat
}

// Beat asks every member host the store reads from that it has not heard
// from within every for its head, each under deadline, and answers each
// host it knows with whether it is there — heard recently, or answered
// the beat. A host that did not answer has its idle pool dropped, so the
// next read of it dials fresh. Hosts the reads just heard from cost
// nothing: no request is made for them.
func (s *Store) Beat(ctx context.Context, every, deadline time.Duration) map[string]bool {
	heard, beat := s.members.due(every)
	out := make(map[string]bool, len(heard)+len(beat))
	for _, host := range heard {
		out[host] = true
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, mb := range beat {
		wg.Add(1)
		go func(mb member) {
			defer wg.Done()
			bctx, cancel := context.WithTimeout(ctx, deadline)
			defer cancel()
			_, err := s.c.Head(bctx, mb.base, mb.ns)
			ok := err == nil || !unanswered(err)
			if ok {
				s.members.answered(mb.host)
			} else {
				s.transport.Reset(mb.host)
			}

			mu.Lock()
			out[mb.host] = ok
			mu.Unlock()
		}(mb)
	}

	wg.Wait()
	return out
}

// Reset drops the store's idle connections to host, or to every host when
// host is "". The next request dials fresh.
func (s *Store) Reset(host string) {
	s.transport.Reset(host)
}

// Hosts lists the member hosts the store reads from, for a test.
func (s *Store) Hosts() []string {
	s.members.mu.Lock()
	defer s.members.mu.Unlock()
	out := make([]string, 0, len(s.members.seen))
	for host := range s.members.seen {
		out = append(out, host)
	}

	sort.Strings(out)
	return out
}

// unanswered reports an error that means the host did not answer at all —
// the request never completed — as opposed to one that answered with a
// refusal or a status. The client hands the former back as *url.Error.
func unanswered(err error) bool {
	var ue *url.Error
	return errors.As(err, &ue) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}

	return u.Host
}
