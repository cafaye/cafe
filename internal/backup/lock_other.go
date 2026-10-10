//go:build !unix

package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// drillLock on a platform with no flock, and the honest difference is stated
// rather than hidden: an exclusive create is the EXISTENCE of a file, so a
// process killed between the create and the close leaves a lock nobody can
// release. flock has no such state because the kernel owns it.
//
// caf ships a Windows binary (.goreleaser.yml lists goos: windows), and rather
// than add a module for one syscall this is the weaker primitive, exactly as
// internal/ledger's lock_other.go is. A drill store on Windows therefore needs
// a person to delete store.lock after a crash, and every error says so in the
// sentence instead of waiting forever for a lock that will not come back.
type drillLock struct {
	file *os.File
	path string
}

// ErrDrillHeld is a lock somebody else has, on a platform where the answer to
// it may be to remove a file by hand.
var ErrDrillHeld = errors.New("held")

func (s *DrillStore) lock() (*drillLock, error) {
	return s.lockWith(0)
}

// tryLock is lock on a platform where the two are the same primitive: an
// exclusive create is already non-blocking, and it is the reason the unix
// build's second primitive buys nothing here that the first does not.
func (s *DrillStore) tryLock() (*drillLock, error) {
	return s.lockWith(0)
}

func (s *DrillStore) lockWith(int) (*drillLock, error) {
	path := filepath.Join(s.dir, "store.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("drill store: create %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: another caf is writing the drill store at %s, and on this platform a lock left behind by a killed caf has to be removed by hand (rm %s)", ErrDrillHeld, s.dir, path)
		}
		return nil, fmt.Errorf("drill store: take the store lock: %w", err)
	}
	return &drillLock{file: file, path: path}, nil
}

// Release drops the lock, by closing the file and removing it.
func (k *drillLock) Release() error {
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
	if removeErr != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("drill store: release the store lock: %w", removeErr)
	}
	return nil
}
