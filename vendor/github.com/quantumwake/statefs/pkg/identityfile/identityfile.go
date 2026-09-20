// Package identityfile is the client-side credential file (RFC-0011
// §12.4): `statefs keygen` writes it, SDKs read it. The PRIVATE key is
// born here and never leaves; only the public half is registered with
// the directory. One small JSON document, shared byte-for-byte by the Go
// and Python SDKs:
//
//	{"username": "...", "alg": "ed25519", "private_key": "<b64 seed>", "public_key": "<b64>"}
package identityfile

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultPath is where SDKs look when nothing else is configured.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".statefs/identity"
	}

	return filepath.Join(home, ".statefs", "identity")
}

// File is the on-disk document.
type File struct {
	Username   string `json:"username"`
	Alg        string `json:"alg"`
	PrivateKey string `json:"private_key"`         // base64 32-byte seed
	PublicKey  string `json:"public_key"`          // base64 32-byte raw public key
	Directory  string `json:"directory,omitempty"` // the installation this key enrolled with
}

// Generate mints a fresh Ed25519 identity file for a username.
func Generate(username string) (File, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return File{}, err
	}

	return File{
		Username:   username,
		Alg:        "ed25519",
		PrivateKey: base64.StdEncoding.EncodeToString(priv.Seed()),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// Private answers the signing key.
func (f File) Private() (ed25519.PrivateKey, error) {
	if f.Alg != "ed25519" {
		return nil, fmt.Errorf("identityfile: unsupported alg %q", f.Alg)
	}

	seed, err := base64.StdEncoding.DecodeString(f.PrivateKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("identityfile: bad private key")
	}

	return ed25519.NewKeyFromSeed(seed), nil
}

// InvitationProofMessage is the exact byte string a host signs to prove
// it holds the private half of the key it is registering:
//
//	SHA-256(token) ‖ raw public key
//
// An invitation (`in_…`) may be reusable and long-lived, so the secret
// alone is not enough: anyone who saw the token could otherwise mint a
// principal with a key of their choosing. Binding the signature to both
// the token and the key makes the proof useless for any other key, and
// the key useless without the token. Server and client MUST build this
// message the same way — it is defined here, once.
func InvitationProofMessage(token string, publicKey []byte) []byte {
	sum := sha256.Sum256([]byte(token))
	return append(sum[:], publicKey...)
}

// InvitationProof signs InvitationProofMessage with this identity's
// private key, base64url (unpadded) — the `proof` field of /auth/enroll.
func (f File) InvitationProof(token string) (string, error) {
	priv, err := f.Private()
	if err != nil {
		return "", err
	}

	pub, err := base64.StdEncoding.DecodeString(f.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", errors.New("identityfile: bad public key")
	}

	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, InvitationProofMessage(token, pub))), nil
}

// Write persists the file with owner-only permissions, creating the
// parent directory.
func Write(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// Read loads a file.
func Read(path string) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}

	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("identityfile: %s: %w", path, err)
	}

	if f.Username == "" || f.PrivateKey == "" {
		return File{}, fmt.Errorf("identityfile: %s: missing username or private_key", path)
	}

	return f, nil
}

// Parse loads a file from inline JSON (the STATEFS_PRIVATE_KEY env
// form for CI runners).
func Parse(raw string) (File, error) {
	var f File
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return File{}, fmt.Errorf("identityfile: inline: %w", err)
	}

	return f, nil
}
