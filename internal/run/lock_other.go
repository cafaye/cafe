//go:build !unix

package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The standard library exposes flock on the unix systems and not on Windows,
// and cafe ships a Windows binary (.goreleaser.yml lists goos: windows). So
// this file uses the atomic exclusive create every platform provides, which
// is a weaker primitive and the difference is stated rather than hidden.
//
// An exclusive-create lock is the EXISTENCE of a file, so a driver killed
// between the create and the close leaves a lock nobody can release — flock
// has no such state because the kernel owns it. The retry below is what
// makes the difference tolerable rather than fatal: the critical sections
// here are microseconds, so a store whose lock file is present waits a
// moment and succeeds, and only a genuinely stuck lock (one left by a killed
// driver on Windows) fails — and it fails with the sentence that names the
// file to remove.
type storeLock struct {
	file *os.File
	path string
}

// lockWait is how long a mutation waits for the store lock before reporting
// ErrHeld. It is short because the sections are short: taking a lease,
// beginning a step, appending an event. A longer wait would only ever be
// spent waiting for a driver that is not coming back.
const lockWait = 2 * time.Second

func (s *Store) take() (*storeLock, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("run store: create %s: %w", s.dir, err)
	}
	path := filepath.Join(s.dir, "store.lock")
	deadline := time.Now().Add(lockWait)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return &storeLock{file: file, path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("run store: take the store lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: the store lock %s is held by another cafe, and on this platform a lock left behind by a killed cafe has to be removed by hand (rm %s)", ErrHeld, path, path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (k *storeLock) release() error {
	if k == nil || k.file == nil {
		return nil
	}
	file, path := k.file, k.path
	k.file = nil
	closeErr := file.Close()
	removeErr := os.Remove(path)
	if closeErr != nil {
		return closeErr
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("run store: release the store lock %s: %w", path, removeErr)
	}
	return nil
}

// hold takes the store lock for the length of one operation and returns the
// release. Every exported mutation goes through it, which is why no two of
// them can interleave.
func (s *Store) hold() (func() error, error) {
	lock, err := s.take()
	if err != nil {
		return nil, err
	}
	return lock.release, nil
}
