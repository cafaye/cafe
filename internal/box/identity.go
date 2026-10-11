package box

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// The two files this package writes, named here because a third spelling of
// either is how a store reads one path and writes another.
const (
	identityFile = "box.json"
	journalFile  = "box-journal.json"
	eventsFile   = "box-events.jsonl"
)

// Identity is a box, and it is the non-secret half of one.
//
// There is deliberately NO private key field. That is the invariant expressed
// in the type rather than in a comment: a struct with no key in it cannot be
// marshalled into a document that carries one, cannot be returned by a command
// that formats a struct with %v or %+v, and cannot be printed by a test that
// round-trips it. A test asserting "no document carries the private key" over
// a type that could hold it is a test that passes until somebody adds a field;
// a type that cannot hold it is the same guarantee that survives the edit.
type Identity struct {
	BoxID      string `json:"box_id"`
	AccountID  string `json:"account_id"`
	PublicKey  string `json:"public_key"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	EnrolledAt string `json:"enrolled_at"`
	Conductor  string `json:"conductor"`
}

// Box is the keypair a box holds: the private half lives in the keyring for the
// life of the process that needs it and is dropped on the floor afterwards,
// and the public half is in the Identity. Nothing in this package writes a Box
// to a file or hands one to a caller that asks for a document.
type Box struct {
	Identity
	private ed25519.PrivateKey
}

// Public is the public key. It is the only half a document may carry.
func (b *Box) Public() ed25519.PublicKey { return b.private.Public().(ed25519.PublicKey) }

// generateBox makes a fresh keypair. ed25519 because the box signs a request
// with it and a signature is the one thing a box is asked for on every
// outbound connection — and because the standard library has it, which is the
// same reason every other dependency in this repository was avoided.
func generateBox() (*Box, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate box keypair: %w", err)
	}
	return &Box{private: private}, nil
}

// publicKeyText is the wire form of a public key: raw standard base64, which
// is what a 32-byte ed25519 key is, unpadded so it survives a URL and a
// YAML scalar unchanged.
func publicKeyText(key ed25519.PublicKey) string {
	return base64.RawStdEncoding.EncodeToString(key)
}

// parsePublicKey reads it back and refuses anything that is not 32 bytes,
// because a key of the wrong length is a key that will fail to verify at the
// far end and the moment to find that out is here.
func parsePublicKey(text string) (ed25519.PublicKey, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("box public key is not base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("box public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// privateKeyText is the keyring form of a private key: raw standard base64 of
// the full 64-byte seed-and-public pair. It is written to the Keyring and to
// nothing else — see ring.go for the whole argument.
func privateKeyText(key ed25519.PrivateKey) string {
	return base64.RawStdEncoding.EncodeToString(key)
}

// parsePrivateKey reads a key out of the keyring.
func parsePrivateKey(text string) (ed25519.PrivateKey, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("box private key in the keyring is not base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("box private key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// ErrNotEnrolled is what every lifecycle verb answers on a machine that has
// never enrolled. It is a sentinel so a caller can tell "you have not enrolled"
// from "your enrollment is broken", which are different instructions.
var ErrNotEnrolled = errors.New("box is not enrolled: run `cafe box enroll --token …` first")

// ErrNoPrivateKey is what `verify` answers when the identity is on disk and its
// key is not in the keyring: the box exists as a record and cannot act as a
// box. It is distinct from ErrNotEnrolled because the remedy is different —
// re-enrolling with a fresh capability mints a new identity, and what is on
// disk is not the thing to keep.
var ErrNoPrivateKey = errors.New("box private key is not in the keyring")
