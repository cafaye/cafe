package agent

import "errors"

// ErrNotRegistered is what a Store answers when there is no session to act on.
// It is a sentinel rather than a bare error because the caller's remedy is a
// different sentence depending on it: an unregistered machine should be told
// to register, not told that its keyring is broken.
var ErrNotRegistered = errors.New("no agent session is registered on this machine")

// ErrNoToken is what a Store answers when a session exists but its token cannot
// be read out of the ring. Absent, unreadable and refused are one answer here
// on purpose: the remedy is identical, so the sentence names it rather than
// sending the reader to work out which of the three happened. The ring's own
// cause is wrapped so an operator can still see WHY, and that cause is a
// description of a failure rather than a value.
var ErrNoToken = errors.New("no usable token for this agent session; run `caf agent register` to make a new one")

// ErrNoBaseURL is what a Store answers when nothing named the identity origin.
// It is a sentinel so the CLI can say "pass --base-url or set CAF_IDENTITY_URL"
// rather than reporting a failure that names no cause.
var ErrNoBaseURL = errors.New("no identity origin: pass --base-url <url> or set CAF_IDENTITY_URL")

// ErrMultipleSessions is what `caf agent login` answers when more than one
// saved session matches the selectors it was given. Choosing one for the caller
// is how a token ends up used against the wrong environment.
var ErrMultipleSessions = errors.New("more than one saved agent session matches; pass --base-url to choose")

// ErrUnknownSession is what a selector that matches nothing answers. It is not
// a usage error: the machine is fine, the registry simply has no such session.
var ErrUnknownSession = errors.New("no saved agent session matches")

// Keyring is where a secret lives. It is three calls wide because that is all
// this package needs from a secret store: put one in, get one out, take one
// out. Everything else — the macOS login keychain, the freedesktop Secret
// Service, a YubiKey — is an implementation of this interface and none of it is
// this package's business.
//
// The seam exists because of invariant 1: an agent's transcript is a log, so
// the one long-lived thing it holds must be reachable by the CLI and by nothing
// else. A store a test can replace is also the only way the "no token ever
// appears in either stream" assertion can be made without a keychain on the
// runner.
type Keyring interface {
	// Get returns the secret stored under service/account.
	Get(service, account string) (string, error)
	// Set stores it, replacing whatever was there.
	Set(service, account, value string) error
	// Delete removes it. Removing an absent entry is not an error.
	Delete(service, account string) error
}

// The keyring's naming, in one place because a second spelling of it is how a
// token ends up written under one name and read under another. The service name
// is the one the box's private key is filed under — one keyring, two subjects —
// and the account prefix keeps the two apart: an agent's token can never
// overwrite a box's key, and a `caf box` command can never reach an agent's
// token by asking for the wrong string.
const (
	// KeyringService is the keyring service name both subjects file under.
	KeyringService = "caf"
	// ringAccountPrefix is the account an agent's token is filed under, with
	// the session name appended so two agents registered against two
	// environments on one laptop do not overwrite each other.
	ringAccountPrefix = "agent/"
)

// ringAccount is the keyring account name for one session's token.
func ringAccount(sessionName string) string { return ringAccountPrefix + sessionName }
