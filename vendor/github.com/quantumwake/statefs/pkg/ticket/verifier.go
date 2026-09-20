// Verifier is the member-side gate: offline ticket verify + request-MAC
// check + a bounded replay window. It holds only the PUBLIC key.
package ticket

import (
	"crypto/ed25519"
	"crypto/sha256"
	"net/http"
	"sync"
	"time"
)

// Verifier validates data-plane requests. Zero-value is unusable; build
// with NewVerifier. Safe for concurrent use.
type Verifier struct {
	pub  ed25519.PublicKey
	skew time.Duration

	mu   sync.Mutex
	seen map[string]int64 // MAC -> unix expiry of its replay slot
}

// NewVerifier builds a verifier for a public key. skew bounds both clock
// drift on expiry and the request-timestamp freshness window.
func NewVerifier(pub ed25519.PublicKey, skew time.Duration) *Verifier {
	if skew <= 0 {
		skew = 30 * time.Second
	}

	return &Verifier{pub: pub, skew: skew, seen: map[string]int64{}}
}

// Authorize checks one request: ticket valid for namespace+verb, the MAC
// binds this exact request, the timestamp is fresh, and the MAC has not
// been seen inside the window (replay). body is the FULL request body
// (the caller has already read it). A thin wrapper over VerifyRequest for
// every caller that has no use for the ticket's own claims.
func (v *Verifier) Authorize(r *http.Request, namespace, verb string, body []byte, now time.Time) error {
	_, err := v.VerifyRequest(r, namespace, verb, body, now)
	return err
}

// VerifyRequest is Authorize plus the verified ticket Payload — RFC-0019
// §15.5 (D-A9, amended 2026-09-13): the member's per-request CORS
// "tighter check" needs the ticket's OWN tenant claim (Payload.Tenant) to
// know which application's origin list narrows this request, and that
// claim is only trustworthy once the signature has already been
// checked — so the caller must not parse the raw ticket header itself
// before or instead of calling this.
func (v *Verifier) VerifyRequest(r *http.Request, namespace, verb string, body []byte, now time.Time) (Payload, error) {
	p, err := Verify(v.pub, r.Header.Get(HeaderTicket), now, v.skew)
	if err != nil {
		return Payload{}, ErrInvalid
	}

	if p.Namespace != namespace || !p.HasVerb(verb) {
		return Payload{}, ErrInvalid
	}

	ts, err := parseTS(r.Header.Get(HeaderTS))
	if err != nil {
		return Payload{}, ErrInvalid
	}

	// Freshness: the sender's timestamp must be within the window — the
	// replay cache below only has to remember one window's worth.
	d := now.Unix() - ts
	if d < 0 {
		d = -d
	}

	if d > int64(v.skew/time.Second) {
		return Payload{}, ErrInvalid
	}

	key, err := MACKey(r.Header.Get(HeaderTicket))
	if err != nil {
		return Payload{}, ErrInvalid
	}

	sum := sha256.Sum256(body)
	mac := r.Header.Get(HeaderMAC)
	if !CheckMAC(key, r.Method, r.URL.Path, sum[:], ts, r.Header.Get(HeaderNonce), mac) {
		return Payload{}, ErrInvalid
	}

	if v.replayed(mac, now) {
		return Payload{}, ErrInvalid
	}

	return p, nil
}

// replayed records a MAC and answers whether it was already seen inside
// its freshness window. The map is swept opportunistically — it is
// bounded by the request rate times the window.
func (v *Verifier) replayed(mac string, now time.Time) bool {
	exp := now.Add(2 * v.skew).Unix()
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, dup := v.seen[mac]; dup {
		return true
	}

	if len(v.seen) > 4096 {
		cut := now.Unix()
		for m, e := range v.seen {
			if e < cut {
				delete(v.seen, m)
			}
		}
	}

	v.seen[mac] = exp
	return false
}

func parseTS(s string) (int64, error) {
	var ts int64
	var err error
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, ErrInvalid
		}

		ts = ts*10 + int64(c-'0')
	}

	if s == "" {
		err = ErrInvalid
	}

	return ts, err
}
