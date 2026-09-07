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
// (the caller has already read it).
func (v *Verifier) Authorize(r *http.Request, namespace, verb string, body []byte, now time.Time) error {
	p, err := Verify(v.pub, r.Header.Get(HeaderTicket), now, v.skew)
	if err != nil {
		return ErrInvalid
	}

	if p.Namespace != namespace || !p.HasVerb(verb) {
		return ErrInvalid
	}

	ts, err := parseTS(r.Header.Get(HeaderTS))
	if err != nil {
		return ErrInvalid
	}

	// Freshness: the sender's timestamp must be within the window — the
	// replay cache below only has to remember one window's worth.
	d := now.Unix() - ts
	if d < 0 {
		d = -d
	}

	if d > int64(v.skew/time.Second) {
		return ErrInvalid
	}

	key, err := MACKey(r.Header.Get(HeaderTicket))
	if err != nil {
		return ErrInvalid
	}

	sum := sha256.Sum256(body)
	mac := r.Header.Get(HeaderMAC)
	if !CheckMAC(key, r.Method, r.URL.Path, sum[:], ts, r.Header.Get(HeaderNonce), mac) {
		return ErrInvalid
	}

	if v.replayed(mac, now) {
		return ErrInvalid
	}

	return nil
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
