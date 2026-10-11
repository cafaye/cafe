package box

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// journalState is where a started enrollment has got to. Three words, and the
// difference between them is the difference between a retry that converges and
// a retry that spends a capability twice.
const (
	// journalPrepared is "a keypair exists and is filed, and the capability has
	// not been presented". Everything after this point is recoverable by
	// replaying the redemption.
	journalPrepared = "prepared"
	// journalRedeemed is "the capability has been presented and box has
	// answered". This is written IMMEDIATELY after the response, before the
	// identity file and before the key is moved to its permanent account,
	// because from here on the only thing that must not happen again is the
	// POST.
	journalRedeemed = "redeemed"
)

// journalPending is the keyring account a not-yet-enrolled keypair is filed
// under. The box does not know its box_id until box says so, so there is
// no better name to use before then; afterwards the key moves to
// `box/<box_id>` and this entry is deleted.
const journalPending = KeyringAccountPending

// The names outside this package has to use, exported because a caller that
// has to look at an interrupted enrollment — a status report, a recovery — needs
// the same name the store used. A second spelling of any of them is how a key
// ends up written under one account and read under another.
const (
	// KeyringAccountPending is the account a not-yet-enrolled keypair is filed
	// under, before the control plane has said which box it belongs to.
	KeyringAccountPending = "box/enrollment-pending"
	// KeyringService is the keyring's service name.
	KeyringService = "cafe"
	// IdentityFileName is the file the box's non-secrets live in.
	IdentityFileName = identityFile
	// JournalFileName is the file an in-progress enrollment lives in.
	JournalFileName = journalFile
	// JournalPrepared is the journal state a retry replays from.
	JournalPrepared = journalPrepared
	// JournalRedeemed is the journal state a retry FINISHES from.
	JournalRedeemed = journalRedeemed
)

// The three things Enroll can conclude. They are strings because they go into
// a document a machine reads, and because the difference between "enrolled"
// and "already enrolled" is the difference between a capability being spent
// and not — which is exactly what an agent retrying a command needs to know
// without parsing a sentence.
const (
	// ActionEnrolled means this attempt took the box from unenrolled to
	// enrolled.
	ActionEnrolled = "enrolled"
	// ActionAlreadyEnrolled means the box was already enrolled and this attempt
	// did nothing at all. It is a SUCCESS: the machine is in the state the
	// operator asked for.
	ActionAlreadyEnrolled = "already-enrolled"
	// ActionRefused means the attempt did not complete and the box is still
	// unenrolled. It is a failure, and it is the honest one: the capability is
	// gone and cafe will not pretend otherwise.
	ActionRefused = "refused"
)

// Store is one box's state: the journal, the identity, the event log, and the
// two seams it needs to touch the world.
//
// It holds NO private key. What it holds is the ability to fetch one from the
// ring, for the duration of one method call, and nothing keeps a reference
// afterwards.
type Store struct {
	// Dir is where box.json, box-journal.json and box-events.jsonl live. It is
	// the operator's config directory (~/.config/cafe), never a temp directory:
	// an enrollment is durable state and a temp directory is not.
	Dir string
	// Ring holds the private key. See ring.go.
	Ring Keyring
	// ControlPlane redeems the capability. See control_plane.go.
	ControlPlane ControlPlane
	// now is the clock. It is a field for the reason every other clock in this
	// repository is: a test that cannot control time is a test that either
	// sleeps on the wall clock or asserts nothing about it.
	now func() time.Time
}

// NewStore defaults the clock and nothing else. A store with no control plane is
// refused at the moment it is used rather than at construction, so a caller
// that only inspects state (`cafe box status` on an enrolled box) does not need
// an endpoint configured at all.
func NewStore(dir string, ring Keyring, controlPlane ControlPlane) *Store {
	return &Store{Dir: dir, Ring: ring, ControlPlane: controlPlane, now: time.Now}
}

func (s *Store) stamp() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// journal is the durable record of an enrollment in progress. It contains no
// token: a capability is a credential, and a credential does not belong in a
// JSON file in the config directory. What it carries is the box's PUBLIC key
// and, once the redemption has happened, the installation facts that came
// back — every one of which is a non-secret.
type journal struct {
	State     string `json:"state"`
	PublicKey string `json:"public_key"`
	BoxID     string `json:"box_id,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Phase     string `json:"phase,omitempty"`
	// ControlPlane is the box service's origin the capability was redeemed
	// against. The JSON key stays `conductor`: it is on-disk state, and a new
	// binary must still read a journal an old one wrote.
	ControlPlane string `json:"conductor,omitempty"`
	StartedAt    string `json:"started_at"`
	RedeemedAt   string `json:"redeemed_at,omitempty"`
}

// EnrollResult is what one enrollment attempt concluded.
type EnrollResult struct {
	Action   string    `json:"action"`
	Identity *Identity `json:"identity,omitempty"`
	// Redemptions is how many capabilities this attempt PRESENTED. It is in the
	// document because it is the number the whole design turns on: a retried
	// sequence must report 0 or 1, and never 2.
	Redemptions int `json:"redemptions"`
	// Recovered is true when the attempt found a redemption already recorded
	// and finished from it, which is the case that makes a retry converge.
	Recovered bool   `json:"recovered"`
	Message   string `json:"message"`
}

func (s *Store) paths(name string) string { return filepath.Join(s.Dir, name) }

// ErrNoBox is what an enroll answers when nothing can redeem a
// capability. It is a sentinel so the CLI can say "configure the endpoint"
// rather than reporting a failure that names no cause.
var ErrNoBox = errors.New("no box configured: pass --box <url> or set CAF_BOX_URL")

// Enroll is the whole sequence: make a keypair, present the capability, record
// what came back. Every step is written down before the next one begins, and
// the rule that orders them is `internal/ledger`'s rule — write the entry
// before the resource — applied to a capability instead of a container.
//
// The sequence, with the state it leaves behind at every point:
//
//  1. keypair generated, stored under `box/enrollment-pending`
//  2. journal written: state=prepared, the public key
//     ---- a crash here is replayed from scratch; nothing is spent
//  3. POST /v1/capabilities/redeem          <-- the irreversible step
//  4. journal written: state=redeemed, the installation facts
//     ---- a crash here converges on retry: the key moves to its permanent
//     ---- account and box.json is written, with NO second redemption
//  5. key moved to `box/<box_id>`, pending entry deleted
//  6. box.json written, journal deleted
//
// Step 3 to step 4 is the one window that cannot be closed from this side, and
// it is stated rather than hidden: if cafe dies between receiving the response
// and writing the journal, the retry presents the token again and box
// refuses it, because box's single-use is enforced in its own database.
// The answer in that case is an honest refusal that names the window and the
// remedy (`cafe box enroll` with a freshly minted token), which is strictly
// better than a client that hid the refusal and reported success.
func (s *Store) Enroll(ctx context.Context, token, controlPlaneURL string) (EnrollResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return EnrollResult{Action: ActionRefused}, fmt.Errorf("cafe box enroll: --token is required; it is the single-use capability minted for this box")
	}
	if s.ControlPlane == nil {
		return EnrollResult{Action: ActionRefused}, ErrNoBox
	}

	if id, ok := s.loadIdentity(); ok {
		s.appendEvent("enroll", id.Phase, "box was already enrolled; the capability was not presented")
		return EnrollResult{
			Action:      ActionAlreadyEnrolled,
			Identity:    id,
			Redemptions: 0,
			Message:     "this box is already enrolled as " + id.BoxID + "; nothing was redeemed",
		}, nil
	}

	jr, _, err := s.loadJournal()
	if err != nil {
		return EnrollResult{Action: ActionRefused}, err
	}

	// The convergence branch. A recorded redemption is finished without
	// touching the network, and the assertion behind it is in
	// store_test.go's table: a control plane that FAILS if it is called at all
	// still produces an enrolled box here.
	if jr != nil && jr.State == journalRedeemed {
		id, err := s.finish(*jr)
		if err != nil {
			return EnrollResult{Action: ActionRefused}, err
		}
		s.appendEvent("enroll", id.Phase, "finished an enrollment whose redemption was already recorded; the capability was not presented again")
		return EnrollResult{
			Action:      ActionEnrolled,
			Identity:    id,
			Redemptions: 0,
			Recovered:   true,
			Message:     "recovered the recorded redemption and wrote the box identity",
		}, nil
	}

	private, err := s.pendingKey(jr)
	if err != nil {
		return EnrollResult{Action: ActionRefused}, err
	}
	public := private.Public().(ed25519.PublicKey)

	if jr == nil {
		jr = &journal{State: journalPrepared, StartedAt: s.stamp().Format(time.RFC3339Nano)}
	}
	jr.PublicKey = publicKeyText(public)
	jr.ControlPlane = controlPlaneURL
	if err := s.writeJournal(jr); err != nil {
		return EnrollResult{Action: ActionRefused}, err
	}

	inst, err := s.ControlPlane.Redeem(ctx, token, public)
	if err != nil {
		// The journal is deliberately left in `prepared`: a transient failure
		// (network, box restarting) must be retryable, and a failed
		// redemption must not look like a completed one.
		s.appendEvent("enroll", "", "redemption refused: "+err.Error())
		return EnrollResult{Action: ActionRefused, Redemptions: 1}, fmt.Errorf("cafe box enroll: %w", err)
	}

	// Record what the redemption bought BEFORE moving the key or writing the
	// identity. If this write succeeds, the capability is spent and its value
	// is durably recorded, which is the whole basis for a converging retry.
	jr.State = journalRedeemed
	jr.BoxID = inst.BoxID
	jr.AccountID = inst.AccountID
	jr.Status = inst.Status
	jr.Phase = inst.Phase
	jr.RedeemedAt = s.stamp().Format(time.RFC3339Nano)
	if err := s.writeJournal(jr); err != nil {
		return EnrollResult{Action: ActionRefused, Redemptions: 1}, err
	}

	id, err := s.finish(*jr)
	if err != nil {
		return EnrollResult{Action: ActionRefused, Redemptions: 1}, err
	}
	s.appendEvent("enroll", id.Phase, "box enrolled as "+id.BoxID)
	return EnrollResult{
		Action:      ActionEnrolled,
		Identity:    id,
		Redemptions: 1,
		Message:     "box enrolled as " + id.BoxID,
	}, nil
}

// pendingKey returns the keypair an in-progress enrollment is using, generating
// and filing one if this is the first attempt.
//
// It REUSES an existing pending key rather than generating a second one. A
// retry that generated a fresh keypair would strand whatever the first attempt
// had already filed, and — worse — would make the box's identity a function of
// how many times an agent ran the command.
func (s *Store) pendingKey(jr *journal) (ed25519.PrivateKey, error) {
	stored, err := s.Ring.Get(ringService, journalPending)
	if err == nil {
		key, perr := parsePrivateKey(stored)
		if perr != nil {
			return nil, fmt.Errorf("cafe box enroll: %w; remove the pending enrollment and start again", perr)
		}
		return key, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("cafe box enroll: read the pending box key: %w", err)
	}
	if jr != nil && jr.State == journalRedeemed {
		// A recorded redemption whose pending key is gone is unrecoverable:
		// the capability is spent, box holds the installation, and the
		// only thing that could finish the job is the key cafe no longer has.
		return nil, fmt.Errorf("cafe box enroll: the pending box key for box %s is not in the keyring; the capability is spent, so re-enroll with a freshly minted token", jr.BoxID)
	}
	fresh, err := generateBox()
	if err != nil {
		return nil, err
	}
	if err := s.Ring.Set(ringService, journalPending, privateKeyText(fresh.private)); err != nil {
		return nil, fmt.Errorf("cafe box enroll: store the pending box key: %w", err)
	}
	return fresh.private, nil
}

// finish converts a recorded redemption into a stored box. It is idempotent:
// every write it makes is either an overwrite of identical bytes or a delete,
// so running it twice reaches the same tree.
func (s *Store) finish(jr journal) (*Identity, error) {
	private, err := s.pendingKey(&jr)
	if err != nil {
		return nil, err
	}
	if got := publicKeyText(private.Public().(ed25519.PublicKey)); got != jr.PublicKey {
		return nil, fmt.Errorf("cafe box enroll: the recorded box key does not match the key in the keyring; the enrollment is inconsistent and must be started again with a freshly minted token")
	}
	// The permanent account first, the pending entry second: a box whose key is
	// filed twice is harmless, a box whose key is filed nowhere is not.
	if err := s.Ring.Set(ringService, ringAccount(jr.BoxID), privateKeyText(private)); err != nil {
		return nil, fmt.Errorf("cafe box enroll: file the box key: %w", err)
	}
	if err := s.Ring.Delete(ringService, journalPending); err != nil {
		return nil, fmt.Errorf("cafe box enroll: clear the pending box key: %w", err)
	}
	id := &Identity{
		BoxID:        jr.BoxID,
		AccountID:    jr.AccountID,
		PublicKey:    jr.PublicKey,
		Status:       jr.Status,
		Phase:        jr.Phase,
		EnrolledAt:   jr.RedeemedAt,
		ControlPlane: jr.ControlPlane,
	}
	if err := s.writeIdentity(id); err != nil {
		return nil, err
	}
	// The journal is removed last, and only once the identity is safely on disk.
	// A store that had no identity and no journal would be a box that cannot
	// enroll again and cannot retry: every resource it made would be orphaned
	// by the very store that was supposed to remember them.
	if err := os.Remove(s.paths(journalFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cafe box enroll: clear the enrollment journal: %w", err)
	}
	return id, nil
}

// --- the two files, read and written as bytes -------------------------------

func (s *Store) writeJSON(name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	raw = append(raw, '\n')
	return writePrivate(s.paths(name), raw)
}

// writePrivate writes a file only the box may read. It is 0600 rather than the
// umask's default because the journal carries the box's public key and the
// account it belongs to, and the directory it is in is the one a stray
// `ls -la ~/.config` reads.
func writePrivate(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, raw, 0o600)
}

func (s *Store) writeIdentity(id *Identity) error {
	if err := s.writeJSON(identityFile, id); err != nil {
		return fmt.Errorf("cafe box enroll: write the box identity: %w", err)
	}
	return nil
}

func (s *Store) writeJournal(jr *journal) error {
	if err := s.writeJSON(journalFile, jr); err != nil {
		return fmt.Errorf("cafe box enroll: write the enrollment journal: %w", err)
	}
	return nil
}

func (s *Store) loadIdentity() (*Identity, bool) {
	raw, err := os.ReadFile(s.paths(identityFile))
	if err != nil {
		return nil, false
	}
	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil || id.BoxID == "" {
		return nil, false
	}
	return &id, true
}

func (s *Store) loadJournal() (*journal, bool, error) {
	raw, err := os.ReadFile(s.paths(journalFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cafe box: read the enrollment journal: %w", err)
	}
	var jr journal
	if err := json.Unmarshal(raw, &jr); err != nil {
		return nil, false, fmt.Errorf("cafe box: the enrollment journal at %s is not readable JSON: %w; remove it and re-enroll with a freshly minted token", s.paths(journalFile), err)
	}
	return &jr, true, nil
}

// Identity is the stored box, or ErrNotEnrolled.
func (s *Store) Identity() (*Identity, error) {
	id, ok := s.loadIdentity()
	if !ok {
		return nil, ErrNotEnrolled
	}
	return id, nil
}

// BoxKey is the box's private key, for the duration of one call. It is not
// returned as a value any caller can keep: the method exists so a signing
// operation can be performed here rather than there, and the reason is
// invariant 1 — a private key that a caller holds is a private key that ends up
// in a log the first time somebody formats the wrong variable.
//
// As of this packet there is no caller: nothing in cafe signs yet, because
// outbound mTLS is `cafe boxd`'s (W2-P7). The method exists so that the day the
// signer arrives, the key is fetched at the point of use rather than threaded
// through three layers of arguments.
func (s *Store) BoxKey(id *Identity) (ed25519.PrivateKey, error) {
	stored, err := s.Ring.Get(ringService, ringAccount(id.BoxID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: box %s", ErrNoPrivateKey, id.BoxID)
		}
		return nil, err
	}
	return parsePrivateKey(stored)
}
