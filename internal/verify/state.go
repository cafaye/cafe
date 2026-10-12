package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The minted-secret ledger: the one piece of state the verifier owns.
//
// WHY THIS EXISTS AT ALL. Secrets are pushed to the box at deploy time and
// never baked into an image (invariant 3: secrets are born where they are
// used). That is right for the image and it leaves a hole in the build: a
// value that was minted by a box and installed on that box is invisible to
// anything that holds the source, so an enclave build — one that reproduces
// the box from source with no credentials anywhere near it — has no way to
// know that COURIER_SECRET_BOX_KEY already exists and is already sealed. It
// mints a second one. A second SECRET_KEY_BASE forks every session cookie
// every user holds, and the only thing standing between "we rebuilt the box"
// and "everybody is logged out" is a record saying the value was already
// minted by the one box that encrypted it.
//
// So the ledger holds, per name: the box that minted it, when, and a
// FINGERPRINT. It never holds the value. A fingerprint is a comparison
// handle — it answers "is the value on this box the one that was minted"
// without ever being able to answer "what is the value", which is the same
// property internal/box.Identity has for the private key, achieved by the
// same means: the type has no field a value could be written into. The
// load-bearing test renders a saved state and searches it for a value it was
// handed, with a control half asserting the NAME is present, because a
// ledger that redacted everything would pass by leaking nothing at all.
//
// IT IS ONE FILE, and it is written the same way every time: no timestamp of
// its own, no hostname, no path. Two runs over one tree produce the same
// bytes, so a diff between two ledgers is a diff between two trees — the
// same argument internal/lock and `cafe gen` make for themselves.

// StateFile is the file the ledger is read from and written to, inside the
// state directory the caller names.
const StateFile = "minted-secrets.json"

// StateSchemaVersion is the version of the ledger's own shape. A file from a
// build that understood a different one is refused by name rather than
// half-read into a shape it was never written as.
const StateSchemaVersion = 1

// ErrMintedByAnotherBox is the refusal that makes the ledger worth having.
// A name minted by one box is that box's value; a second box minting the
// same name is two boxes holding one name, and the fork the ledger exists
// to prevent. It is an error rather than a silent replace because the
// alternative is a rotation nobody was told happened.
var ErrMintedByAnotherBox = errors.New("this secret was minted by another box; one name, one box")

// Record is one minted value, remembered by its fingerprint and by the box
// that minted and encrypted it. There is no field a value could be written
// into, which is a property of the type rather than of a comment.
type Record struct {
	// Name is the key's own name: COURIER_SECRET_BOX_KEY, SECRET_KEY_BASE.
	Name string `json:"name"`
	// Class is the inventory's word for the entry. Rotatable is the class
	// this ledger exists for: a value a box can mint again.
	Class string `json:"class"`
	// BoxID is the one box that minted the value AND sealed it. It is
	// named so an enclave build can tell that the value is not its to
	// mint, and so a second box claiming the name is a refusal.
	BoxID string `json:"box_id"`
	// MintedAt is when the minting box minted it. Supplied by the caller
	// rather than read from a clock, so the file is a function of the run.
	MintedAt time.Time `json:"minted_at"`
	// Fingerprint is the sha256 of the value, hex. A comparison handle and
	// not a credential: it cannot be turned back into the value, and it is
	// what lets a deploy that holds the value confirm it is the recorded
	// one without reading it out of anywhere.
	Fingerprint string `json:"fingerprint"`
	// SealedTo names where the control plane holds the value's envelope —
	// the box's own public key, spelled so a reader knows which key opens
	// it without being told any part of the value.
	SealedTo string `json:"sealed_to,omitempty"`
}

// State is the ledger: every value a box minted on this system, by name, by
// box, by fingerprint.
type State struct {
	SchemaVersion int      `json:"schema_version"`
	Records       []Record `json:"records,omitempty"`
}

// LoadState reads the ledger from a directory. A directory with no ledger in
// it is the ordinary first state — no box has minted anything yet — and that
// is not an error. A ledger that cannot be parsed, or that a newer build
// wrote, is refused by name, because a half-read ledger is worse than an
// absent one: it would answer "already minted" for a name it never read.
func LoadState(dir string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return &State{SchemaVersion: StateSchemaVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the minted-secret ledger: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("read the minted-secret ledger: %s is not a ledger: %w", StateFile, err)
	}
	if s.SchemaVersion > StateSchemaVersion {
		return nil, fmt.Errorf("read the minted-secret ledger: %s declares schema version %d and this build knows %d", StateFile, s.SchemaVersion, StateSchemaVersion)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = StateSchemaVersion
	}
	return &s, nil
}

// Save writes the ledger back. The directory is created 0700 and the file
// 0600, because the ledger is not a credential store and a fingerprint is
// not a credential — but a file that says which names exist is still an
// inventory, and an inventory is not for everybody.
func (s *State) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the state directory: %w", err)
	}
	s.SchemaVersion = StateSchemaVersion
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("write the minted-secret ledger: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, StateFile), raw, 0o600); err != nil {
		return fmt.Errorf("write the minted-secret ledger: %w", err)
	}
	return nil
}

// Mint records a value a box minted. The VALUE ARRIVES HERE AND GOES NO
// FURTHER: it is fingerprinted and dropped, and the returned Record and the
// file both hold the fingerprint and not the bytes. That is the one call in
// this package that accepts a value at all, and its signature is the
// discipline — the value is a parameter, it is never stored on the receiver.
//
// A record for this name that another box minted is refused with
// ErrMintedByAnotherBox: one name, one box. A record the SAME box minted is
// replaced, which is what a rotation by the minting box is, and which is the
// only second mint this ledger permits. The minting box's own identity is
// the argument, because "who minted this" is the question the whole file
// exists to answer.
func (s *State) Mint(name, class, boxID string, value []byte, now time.Time) (Record, error) {
	if existing, ok := s.Minted(name); ok && existing.BoxID != boxID {
		return Record{}, fmt.Errorf("%w: %s was minted by box %s, and box %s is asking to mint it again", ErrMintedByAnotherBox, name, existing.BoxID, boxID)
	}
	rec := Record{
		Name:        name,
		Class:       class,
		BoxID:       boxID,
		MintedAt:    now,
		Fingerprint: Fingerprint(value),
		SealedTo:    "box " + boxID + " public key",
	}
	for i := range s.Records {
		if s.Records[i].Name == name {
			s.Records[i] = rec
			return rec, nil
		}
	}
	s.Records = append(s.Records, rec)
	return rec, nil
}

// Minted is the question an enclave build asks before it mints anything: has
// a box already minted this name? The answer is the record — which box, when,
// and against which key it is sealed — and the honest answer to it is either
// "yes, and it is not yours to mint" or "no", which is what makes the build
// able to skip re-proving a secret it never baked.
func (s *State) Minted(name string) (Record, bool) {
	for _, r := range s.Records {
		if r.Name == name {
			return r, true
		}
	}
	return Record{}, false
}

// Proven answers whether a value the caller already holds is the one the
// ledger recorded. It is the deploy path's question — the tool that pushes
// the secret to the box holds the value for exactly as long as it takes to
// push it, and confirming what it is about to push is the recorded value is
// a check it can make without reading anything back out of the cluster. It
// takes the value as a parameter for the same reason Mint does: nothing in
// this package stores one.
func (s *State) Proven(name string, value []byte) bool {
	rec, ok := s.Minted(name)
	return ok && rec.Fingerprint == Fingerprint(value)
}

// Fingerprint is the sha256 of a secret value, hex-encoded. It exists as an
// exported function because the caller that holds the value needs a way to
// compute the same handle the ledger records, and because Mint is not the
// only place the comparison is made. It is a comparison handle and not a
// password hash: the threat it answers is a copy of the file being read by
// somebody it was not meant for, and a digest is enough to make that copy
// useless — it cannot be reversed into the value it stands for.
func Fingerprint(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
