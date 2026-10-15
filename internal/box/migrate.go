package box

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrBoxStoreDiverged is what the migration answers when the box-owned store
// and the legacy user-config-dir store both hold a box identity and they are
// not the same bytes. It is a sentinel so a caller can tell "two owners wrote
// here" from any I/O failure, and the detail names both paths: silently
// preferring one would be choosing which box this machine is.
var ErrBoxStoreDiverged = errors.New("box store diverged")

// MigrateStore moves a pre-box-owned enrollment from legacyDir into dir, once.
// It returns a sentence naming what moved and from where, or "" when there was
// nothing to do — a second run over a migrated tree moves nothing.
//
// The rule is per file, and it is the same rule for every file: both sides
// present with different bytes is a refusal (ErrBoxStoreDiverged), the new
// side missing is a move, anything else is left alone. A move is a rename and
// the source is removed — never a copy — because the keyring entries carry
// the box's private key and it must not exist in two places.
//
// box.json moves LAST. Its presence in the new store is the "migrated" marker
// the next run reads, so a process killed mid-move re-runs the migration and
// converges instead of stranding a half-moved enrollment. The keyring moves
// FIRST for the same reason in the other direction: a pending key with no
// journal yet is resumed, while a journal with no key is refused by name.
func MigrateStore(dir, legacyDir string) (string, error) {
	if sameDir(dir, legacyDir) {
		return "", nil
	}
	moved, err := migrateFiles(dir, legacyDir)
	if err != nil {
		return "", err
	}
	if len(moved) == 0 {
		return "", nil
	}
	return fmt.Sprintf("migrated the box store from %s to %s (%s)", legacyDir, dir, strings.Join(moved, ", ")), nil
}

// sameDir is whether two store paths name one directory. An override that
// points at the legacy location is not a migration: there is nowhere to move
// to, and a rename onto itself must never delete the file it just read.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return aa == bb
}

// migrateFiles moves the box's files and keyring entries. The order is the
// design: keyring first, the identity document last.
func migrateFiles(dir, legacyDir string) ([]string, error) {
	var moved []string
	// The identity divergence is decided before anything moves: a refusal
	// must leave both stores exactly as they were, and the per-file checks
	// below run after earlier files have already moved. The identity is the
	// one file whose divergence refuses the whole migration rather than one
	// row of it.
	newID, newOK := readStoreFile(dir, identityFile)
	legacyID, legacyOK := readStoreFile(legacyDir, identityFile)
	if newOK && legacyOK && !bytes.Equal(newID, legacyID) {
		return nil, fmt.Errorf("%w: %s and %s hold different box identities; move one aside by hand and re-run",
			ErrBoxStoreDiverged, filepath.Join(dir, identityFile), filepath.Join(legacyDir, identityFile))
	}
	if km, err := migrateKeyring(dir, legacyDir); err != nil {
		return nil, err
	} else {
		moved = append(moved, km...)
	}
	// The credential and the journal travel with the identity: a box.json
	// naming a key that stayed behind is a box that cannot dial.
	for _, name := range []string{PrivateKeyFileName, CertificateFileName, journalFile} {
		m, err := migrateOneFile(dir, legacyDir, name)
		if err != nil {
			return nil, err
		}
		moved = append(moved, m...)
	}
	if m, err := migrateEvents(dir, legacyDir); err != nil {
		return nil, err
	} else {
		moved = append(moved, m...)
	}
	// The identity moves last: its presence in the new store is the
	// "migrated" marker the next run reads, so a process killed mid-move
	// re-runs the migration and converges instead of stranding a half-moved
	// enrollment. When both sides already agree, this converges to the new
	// side's copy like every other file.
	m, err := migrateOneFile(dir, legacyDir, identityFile)
	if err != nil {
		return nil, err
	}
	moved = append(moved, m...)
	return moved, nil
}

// readStoreFile reads one store file, reporting absence as (nil, false). A
// present-but-unreadable file is a fault rather than an absence: treating it
// as absent would migrate around it and strand it.
func readStoreFile(dir, name string) ([]byte, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// migrateOneFile moves one named file when the new store lacks it. Both sides
// present with different bytes is the same refusal as the identity's: the
// file is part of the enrollment and two versions of it is two enrollments.
// Both present and equal converges to one copy — the legacy duplicate is
// removed — so a second run finds nothing to do.
func migrateOneFile(dir, legacyDir, name string) ([]string, error) {
	newRaw, newOK := readStoreFile(dir, name)
	legacyRaw, legacyOK := readStoreFile(legacyDir, name)
	if !legacyOK {
		return nil, nil
	}
	if newOK {
		if !bytes.Equal(newRaw, legacyRaw) {
			return nil, fmt.Errorf("%w: %s and %s hold different %s; move one aside by hand and re-run",
				ErrBoxStoreDiverged, filepath.Join(dir, name), filepath.Join(legacyDir, name), name)
		}
		if err := os.Remove(filepath.Join(legacyDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove the migrated %s: %w", name, err)
		}
		return []string{name + " (duplicate removed)"}, nil
	}
	if err := moveFile(filepath.Join(legacyDir, name), filepath.Join(dir, name)); err != nil {
		return nil, err
	}
	return []string{name}, nil
}

// migrateEvents moves the append-only event log. When both sides hold one,
// the legacy lines are appended to the new log and the legacy file removed:
// history is preserved in one place rather than split across two a reader
// would have to know to concatenate.
func migrateEvents(dir, legacyDir string) ([]string, error) {
	legacyRaw, legacyOK := readStoreFile(legacyDir, eventsFile)
	if !legacyOK {
		return nil, nil
	}
	if _, newOK := readStoreFile(dir, eventsFile); !newOK {
		if err := moveFile(filepath.Join(legacyDir, eventsFile), filepath.Join(dir, eventsFile)); err != nil {
			return nil, err
		}
		return []string{eventsFile}, nil
	}
	f, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("append the legacy event log: %w", err)
	}
	if _, err := f.Write(legacyRaw); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("append the legacy event log: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("append the legacy event log: %w", err)
	}
	if err := os.Remove(filepath.Join(legacyDir, eventsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove the migrated %s: %w", eventsFile, err)
	}
	return []string{eventsFile + " (appended)"}, nil
}

// boxRingPrefix is the filename prefix every box keyring entry carries:
// service + "-" + the account with separators flattened, and every box
// account starts with "box/". It is derived the way FileRing derives it, so
// the migration and the ring cannot disagree about which entries are the
// box's — and so the agent's token, filed under a different account, is never
// touched.
func boxRingPrefix() string { return ringService + "-" + flatten(ringAccountPrefix) }

// migrateKeyring moves the box's keyring entries. Per entry the rule is the
// file rule: both sides with different bytes refuse, both with equal bytes
// converge to the new side's copy, the new side missing is a move. Entries
// that are not the box's are never listed and never moved.
func migrateKeyring(dir, legacyDir string) ([]string, error) {
	legacyRing := filepath.Join(legacyDir, "keyring")
	entries, err := os.ReadDir(legacyRing)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list the legacy keyring: %w", err)
	}
	var moved []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), boxRingPrefix()) {
			continue
		}
		newPath := filepath.Join(dir, "keyring", e.Name())
		legacyPath := filepath.Join(legacyRing, e.Name())
		legacyRaw, err := os.ReadFile(legacyPath)
		if err != nil {
			return nil, fmt.Errorf("read the legacy keyring entry %s: %w", e.Name(), err)
		}
		newRaw, err := os.ReadFile(newPath)
		switch {
		case err == nil && !bytes.Equal(newRaw, legacyRaw):
			return nil, fmt.Errorf("%w: the box key %s exists in both %s and %s with different values; move one aside by hand and re-run",
				ErrBoxStoreDiverged, e.Name(), filepath.Join(dir, "keyring"), legacyRing)
		case err == nil:
			if rerr := os.Remove(legacyPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				return nil, fmt.Errorf("remove the migrated key %s: %w", e.Name(), rerr)
			}
			moved = append(moved, "keyring/"+e.Name()+" (duplicate removed)")
		default:
			if err := moveFile(legacyPath, newPath); err != nil {
				return nil, err
			}
			moved = append(moved, "keyring/"+e.Name())
		}
	}
	return moved, nil
}

// moveFile renames src onto dst, creating the destination directory 0700
// first so a key never lands for an instant in a world-readable one. The
// cross-filesystem fallback preserves the source's mode bit for bit: a copy
// that forgot it would leave a private key world-readable.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("move %s to %s: %w", src, dst, err)
	}
	st, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("move %s to %s: %w", src, dst, err)
	}
	if err := os.WriteFile(dst, raw, st.Mode().Perm()); err != nil {
		return fmt.Errorf("move %s to %s: %w", src, dst, err)
	}
	if err := os.Remove(src); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("move %s to %s: remove the source: %w", src, dst, err)
	}
	return nil
}
