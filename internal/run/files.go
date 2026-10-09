package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// readFile reads a file, answering nil when it does not exist. A missing
// run, log or lease is a normal state — a box that has just enrolled has
// none — so "not there" is an empty answer rather than an error, and every
// caller decides what empty means.
func readFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("run store: read %s: %w", path, err)
	}
	return raw, nil
}

// writeAtomic writes by rename. The run document is rewritten on every
// transition, and a rewrite that was not atomic would leave a torn file the
// next driver cannot read — which for this package is the one failure mode
// that would turn a resumable install into an unreadable one.
//
// What this is and is not: rename is atomic within a filesystem, so a
// process that dies mid-write leaves either the old document or the new one.
// It is not fsync'd, so a machine that loses power can lose the last write.
// That is the honest limit and it is stated rather than hidden: the failure
// this package exists for is a driver process that stops, not a box that
// loses the machine.
func writeAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("run store: encode %s: %w", path, err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("run store: write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("run store: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("run store: write %s: %w", path, err)
	}
	// 0600, and the directory 0700: this is the box's install state, and a
	// state file a stray `ls` reads is a state file a stray `ls` can learn
	// the install's shape from.
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("run store: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("run store: write %s: %w", path, err)
	}
	return nil
}

// openAppend opens the log for appending and for nothing else. The mode is
// the append-only guarantee's physical half: O_APPEND is the kernel's
// promise that a write lands at the end, so two appenders cannot overwrite
// each other's lines even without the lock.
func openAppend(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("run store: write %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("run store: open the event log %s: %w", path, err)
	}
	return f, nil
}

// removeIfPresent deletes a file that may not be there. A sweeper and a
// finishing driver that both decided a lease was over must not turn that
// race into a failure.
func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("run store: remove %s: %w", path, err)
	}
	return nil
}
