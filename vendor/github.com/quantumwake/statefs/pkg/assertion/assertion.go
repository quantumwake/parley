// Package assertion is the wire format of the public_key authenticator's
// proof (RFC-0011 §12.2, 7b), shared by every client (SDK, CLI) and the
// directory: the client signs {sub, aud, iat, nonce} with a private key
// that never leaves its machine; the directory verifies against the
// identity's REGISTERED public keys (cluster/pkg/auth). Algorithm-agile:
// the registered key row carries alg; this package only shapes bytes.
package assertion

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Audience pins an assertion to the token exchange — a signed blob
// captured elsewhere cannot be replayed as a login.
const Audience = "statefs/token"

// Assertion is the signed payload.
type Assertion struct {
	Sub   string `json:"sub"` // username
	Aud   string `json:"aud"`
	IAT   int64  `json:"iat"` // unix seconds
	Nonce string `json:"nonce"`
}

// ErrMalformed is the parse refusal.
var ErrMalformed = errors.New("assertion: malformed")

// Sign produces base64url(json).base64url(signature) for a username with
// an Ed25519 private key and a fresh nonce.
func Sign(priv ed25519.PrivateKey, username string, now time.Time) string {
	var n [8]byte
	_, _ = rand.Read(n[:])
	body, _ := json.Marshal(Assertion{Sub: username, Aud: Audience, IAT: now.Unix(), Nonce: hex.EncodeToString(n[:])})
	sig := ed25519.Sign(priv, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Parse splits an assertion WITHOUT verifying — the verifier needs the
// subject first to load its keys.
func Parse(raw string) (Assertion, []byte, []byte, error) {
	i := strings.IndexByte(raw, '.')
	if i <= 0 || i == len(raw)-1 {
		return Assertion{}, nil, nil, ErrMalformed
	}

	body, err := base64.RawURLEncoding.DecodeString(raw[:i])
	if err != nil {
		return Assertion{}, nil, nil, ErrMalformed
	}

	sig, err := base64.RawURLEncoding.DecodeString(raw[i+1:])
	if err != nil {
		return Assertion{}, nil, nil, ErrMalformed
	}

	var a Assertion
	if err := json.Unmarshal(body, &a); err != nil {
		return Assertion{}, nil, nil, ErrMalformed
	}

	return a, body, sig, nil
}
