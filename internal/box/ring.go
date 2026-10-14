package box

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is what a Keyring answers when it holds no such entry. It is a
// sentinel rather than a nil string because "absent" and "present but empty"
// are different failures and only one of them is a reason to generate a key.
var ErrNotFound = errors.New("keyring: no such entry")

// Keyring is where a secret lives. It is two calls wide because that is all
// this package needs from a secret store: put one in, get one out. Everything
// else — the macOS login keychain, the freedesktop Secret Service, a YubiKey —
// is an implementation of this interface and none of it is this package's
// business.
//
// The seam exists because of invariant 1: an agent's transcript is a log, so
// the one long-lived thing a box holds must be reachable by the CLI and by
// nothing else. A store a test can replace is also the only way
// `TestNoDocumentEverCarriesThePrivateKey` can render every answer without a
// keychain present.
type Keyring interface {
	// Get returns the secret stored under service/account, or ErrNotFound.
	Get(service, account string) (string, error)
	// Set stores it, replacing whatever was there.
	Set(service, account, value string) error
	// Delete removes it. Removing an absent entry is not an error.
	Delete(service, account string) error
}

// The keyring's naming, in one place because a second spelling of it is how a
// key ends up written under one name and read under another.
// ringService is the keyring service name this package files everything
// under. It is the same string as the exported KeyringService, and keeping the
// two in sync by having only one of them is the point.
const ringService = KeyringService

// ringAccountPrefix is the account a box's private key is filed under, and the
// box ID is appended to it so that two boxes enrolled on one machine — a
// developer with a spare box, a CI runner with two — do not overwrite each
// other's identity.
const ringAccountPrefix = "box/"

// ringAccount is the keyring account name for one box's private key.
func ringAccount(boxID string) string { return ringAccountPrefix + boxID }

// FileRing is the default Keyring: a directory of 0600 files, one per entry,
// each holding raw bytes and nothing else.
//
// It is NOT the operating system's secret store, and saying so plainly is the
// point. A file in a hidden directory is only as private as the home directory
// around it, which on a developer's laptop is the honest baseline and on a
// production box is what systemd's own credential handling does anyway. The
// reason it is the default rather than a shell-out to `security` or
// `secret-tool` is the rule this repository holds everywhere else: a
// subprocess is not free, and the one thing cafe must never do is fail to store
// a key because a keychain helper was not on PATH. The interface is the
// contract; an OS-keychain ring is a later packet that changes this file's
// constructor call and nothing else.
//
// What it must never become is a second copy of the config file. `box.json`
// lives in the same directory tree and holds no secret; `keyring/` is created
// separately with 0700 so the two are not confused by a reader.
type FileRing struct {
	// Dir is where the entries live. It is created 0700 on first write, and a
	// Set on an existing directory with looser permissions is REFUSED rather
	// than silently tightened: a directory whose mode we cannot guarantee is a
	// directory we should not put a key in, and tightening it is a change to
	// the machine the operator did not ask for.
	Dir string
}

// entryPath is the file one account's secret lives in. The name is the account
// with the separators flattened, because a keychain account may name a path
// and a filename may not contain one.
func (r FileRing) entryPath(service, account string) string {
	return filepath.Join(r.Dir, service+"-"+flatten(account))
}

func flatten(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// Get reads one entry. An unreadable directory and an absent entry are
// different answers: the first is a fault, the second is ErrNotFound.
func (r FileRing) Get(service, account string) (string, error) {
	b, err := os.ReadFile(r.entryPath(service, account))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s/%s", ErrNotFound, service, account)
		}
		return "", fmt.Errorf("read keyring entry %s/%s: %w", service, account, err)
	}
	return string(b), nil
}

// Set writes one entry, atomically and with the mode the secret requires.
//
// The write is a temp file plus a rename, not a truncate-and-write: a cafe that
// died mid-write would otherwise leave a half key on disk, and a half key is
// worse than no key because the next run cannot tell a truncated base64 string
// from a valid one.
func (r FileRing) Set(service, account, value string) error {
	if err := ensureRingDir(r.Dir); err != nil {
		return err
	}
	path := r.entryPath(service, account)
	tmp, err := os.CreateTemp(r.Dir, ".entry-*")
	if err != nil {
		return fmt.Errorf("create keyring entry %s/%s: %w", service, account, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(value); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write keyring entry %s/%s: %w", service, account, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mode keyring entry %s/%s: %w", service, account, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close keyring entry %s/%s: %w", service, account, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("install keyring entry %s/%s: %w", service, account, err)
	}
	return nil
}

// Delete removes one entry. An absent entry is a success: the caller wants the
// key gone, and it already is.
func (r FileRing) Delete(service, account string) error {
	if err := os.Remove(r.entryPath(service, account)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete keyring entry %s/%s: %w", service, account, err)
	}
	return nil
}

// ensureRingDir creates the store if it is missing and refuses it if it is
// looser than 0700. The refusal is the load-bearing half: a directory that
// world-readable peers can walk is not a place to leave a private key, and
// silently chmod-ing it is the kind of helpfulness that ends with an operator
// wondering why their umask changed.
func ensureRingDir(dir string) error {
	st, err := os.Stat(dir)
	if err == nil {
		if !st.IsDir() {
			return fmt.Errorf("keyring path %s is not a directory", dir)
		}
		if st.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("keyring directory %s is %#o and is writable or readable by others; cafe will not store a key there", dir, st.Mode().Perm())
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat keyring directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create keyring directory %s: %w", dir, err)
	}
	return nil
}
