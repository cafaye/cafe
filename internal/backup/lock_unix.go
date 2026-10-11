//go:build unix

package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// drillLock is one exclusive hold on the drill store.
//
// It is flock for the reason internal/ledger's is: flock is owned by the open
// file description, so the kernel drops it when the holder dies. A crashed cafe
// needs no janitor, and a lock left behind by a killed process is not stale —
// it is gone. That property matters more here than it does there, because a
// drill takes minutes and the machine that runs one is a machine somebody
// Ctrl-C's.
type drillLock struct {
	file *os.File
	name string
}

// ErrDrillHeld is a lock somebody else has. It is a distinct sentinel because
// the fix differs from every other drill failure: nothing is broken, and the
// answer is to wait for the other drill or to leave it alone.
var ErrDrillHeld = errors.New("held")

func (s *DrillStore) lock() (*drillLock, error) {
	return s.lockWith(syscall.LOCK_EX)
}

// tryLock is lock that will not wait, and it exists for the same reason
// internal/ledger's TryLock does: it is how a caller answers "is another cafe
// writing this store" without hanging behind a lock it cannot see. A drill
// takes minutes, so a second drill that BLOCKED would be a command that looked
// broken for the length of the first one.
func (s *DrillStore) tryLock() (*drillLock, error) {
	return s.lockWith(syscall.LOCK_EX | syscall.LOCK_NB)
}

func (s *DrillStore) lockWith(flags int) (*drillLock, error) {
	path := filepath.Join(s.dir, "store.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("drill store: create %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("drill store: take the store lock: %w", err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|flags)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		break
	}
	if err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: another cafe is writing the drill store at %s", ErrDrillHeld, s.dir)
		}
		return nil, fmt.Errorf("drill store: take the store lock: %w", err)
	}
	return &drillLock{file: file, name: path}, nil
}

// Release drops the lock. It is safe to call twice.
func (k *drillLock) Release() error {
	if k == nil || k.file == nil {
		return nil
	}
	file := k.file
	k.file = nil
	unlock := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlock != nil {
		return fmt.Errorf("drill store: release the store lock: %w", unlock)
	}
	return closeErr
}
