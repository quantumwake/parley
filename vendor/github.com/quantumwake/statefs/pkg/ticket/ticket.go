// Package ticket is the data-plane capability (RFC-0011 §7, mechanism
// O-B+): the directory MINTS a short-TTL Ed25519-signed grant ticket
// binding an identity to one namespace and a verb set; a member VERIFIES
// it offline (public key only — a compromised member can mint nothing).
// Request integrity rides an HMAC whose key both sides derive from the
// ticket's signature (HKDF) — no extra secret ever distributed — over
// method|path|body-hash|timestamp, replay-bounded by a freshness window.
package ticket

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Payload is the signed grant: who, over what, doing which, until when.
type Payload struct {
	Sub       string   `json:"sub"`    // username (display/audit)
	User      string   `json:"user"`   // user id
	Tenant    string   `json:"tenant"` // tenant slug
	Namespace string   `json:"ns"`
	Verbs     []string `json:"verbs"` // subset of {read, write}
	Exp       int64    `json:"exp"`   // unix seconds

	// Act/ActMembership (RFC-0019 amendment §17, D-A12): the acting
	// application's own username and _cluster membership id, carried
	// through from the minting identity's auth.Identity.Actor/
	// ActorMembership — empty for a ticket minted by an ordinary
	// (non-act_as) token. A member logging a write then names both the
	// identity and the application on whose behalf it acted: nothing
	// happens to a customer's data without a row naming who did it and
	// for whom. Never an authorization input — Sub/User's grants are the
	// only thing that opens a namespace; these two ride for the record.
	Act           string `json:"act,omitempty"`
	ActMembership string `json:"act_membership,omitempty"`
}

// HasVerb answers whether the grant covers a verb.
func (p Payload) HasVerb(verb string) bool {
	for _, v := range p.Verbs {
		if v == verb {
			return true
		}
	}

	return false
}

// ErrInvalid is the uniform refusal — callers never learn WHY a ticket
// failed (signature, shape, expiry all answer the same).
var ErrInvalid = errors.New("ticket: invalid")

// Signer is the VAULT PORT: minting needs a signature, never the private
// key. The v1 implementation wraps an env/Secret-provided Ed25519 seed;
// a real KMS / Vault transit engine / cloud secret manager satisfies the
// same interface without the key ever leaving it.
type Signer interface {
	Sign(ctx context.Context, message []byte) ([]byte, error)
}

// MintSigned signs a payload through the vault port:
// base64url(json).base64url(signature).
func MintSigned(ctx context.Context, s Signer, p Payload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}

	sig, err := s.Sign(ctx, body)
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Mint signs a payload with a locally held key (tests, tools).
func Mint(priv ed25519.PrivateKey, p Payload) (string, error) {
	return MintSigned(context.Background(), localSigner{priv}, p)
}

// localSigner adapts a raw private key to the vault port.
type localSigner struct{ priv ed25519.PrivateKey }

func (l localSigner) Sign(_ context.Context, message []byte) ([]byte, error) {
	return ed25519.Sign(l.priv, message), nil
}

// Verify checks signature and expiry (exp+skew is the hard stop; skew
// also forgives a slightly-fast member clock on not-yet-valid... there is
// no nbf — mint time is now, TTL is minutes).
func Verify(pub ed25519.PublicKey, raw string, now time.Time, skew time.Duration) (Payload, error) {
	body, sig, err := split(raw)
	if err != nil {
		return Payload{}, ErrInvalid
	}

	if !ed25519.Verify(pub, body, sig) {
		return Payload{}, ErrInvalid
	}

	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Payload{}, ErrInvalid
	}

	if now.After(time.Unix(p.Exp, 0).Add(skew)) {
		return Payload{}, ErrInvalid
	}

	return p, nil
}

// MACKey derives the request-MAC key from the ticket's SIGNATURE via
// HKDF-SHA256 (extract+expand, RFC 5869): only a holder of the full
// ticket can derive it, and both sides derive it independently — the
// user's HKDF direction, with the ticket as the shared secret.
func MACKey(raw string) ([]byte, error) {
	_, sig, err := split(raw)
	if err != nil {
		return nil, ErrInvalid
	}

	// Extract: PRK = HMAC(salt, ikm).
	ext := hmac.New(sha256.New, []byte("statefs-ticket-mac-v1"))
	ext.Write(sig)
	prk := ext.Sum(nil)

	// Expand (single 32-byte block): T(1) = HMAC(PRK, info || 0x01).
	exp := hmac.New(sha256.New, prk)
	exp.Write([]byte("mac"))
	exp.Write([]byte{1})
	return exp.Sum(nil), nil
}

// ComputeMAC binds one request to the ticket: method, path, body hash,
// the sender's timestamp, and a per-request nonce — the nonce keeps two
// legitimate identical requests (scan pages in the same second) from
// colliding in the member's replay cache.
func ComputeMAC(key []byte, method, path string, bodySHA256 []byte, ts int64, nonce string) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%s\n%s\n%s\n%d\n%s", method, path, hex.EncodeToString(bodySHA256), ts, nonce)
	return hex.EncodeToString(m.Sum(nil))
}

// CheckMAC verifies a presented MAC (constant-time).
func CheckMAC(key []byte, method, path string, bodySHA256 []byte, ts int64, nonce, presented string) bool {
	want := ComputeMAC(key, method, path, bodySHA256, ts, nonce)
	return subtle.ConstantTimeCompare([]byte(want), []byte(presented)) == 1
}

// split decodes the two dot-joined base64url parts.
func split(raw string) (body, sig []byte, err error) {
	i := strings.IndexByte(raw, '.')
	if i <= 0 || i == len(raw)-1 {
		return nil, nil, ErrInvalid
	}

	if body, err = base64.RawURLEncoding.DecodeString(raw[:i]); err != nil {
		return nil, nil, ErrInvalid
	}

	if sig, err = base64.RawURLEncoding.DecodeString(raw[i+1:]); err != nil {
		return nil, nil, ErrInvalid
	}

	return body, sig, nil
}

// ── request headers (the wire contract, shared by SDKs and members) ─────

// Header names for the ticket transport.
const (
	HeaderTicket = "X-Statefs-Ticket"
	HeaderTS     = "X-Statefs-TS"
	HeaderNonce  = "X-Statefs-Nonce"
	HeaderMAC    = "X-Statefs-MAC"
)

// Verbs.
const (
	VerbRead  = "read"
	VerbWrite = "write"
)
