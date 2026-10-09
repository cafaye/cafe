//go:build unix

package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// storeLock is the mutual-exclusion primitive every mutation in this package
// takes. It is the ledger's flock, and the reason is the ledger's: the lock
// is owned by the open file description, so the kernel drops it when the
// holder dies — a store left locked by a killed driver is not stuck, it is
// free.
//
// It is a critical-section lock and NOT the lease. The lease is a record
// with an expiry, which outlives the lock because it has to: a driver that
// is alive and wedged still holds its flock, and only an expiry can take the
// run off it. The lock exists so that two drivers' read-modify-write cycles
// never interleave; it is held for the length of one operation and nothing
// else.
type storeLock struct {
	file *os.File
}

func (s *Store) take() (*storeLock, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("run store: create %s: %w", s.dir, err)
	}
	path := filepath.Join(s.dir, "store.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("run store: open %s: %w", path, err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		break
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("run store: take the store lock %s: %w", path, err)
	}
	return &storeLock{file: file}, nil
}

func (k *storeLock) release() error {
	if k == nil || k.file == nil {
		return nil
	}
	file := k.file
	k.file = nil
	unlock := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlock != nil {
		return fmt.Errorf("run store: release the store lock: %w", unlock)
	}
	return closeErr
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
