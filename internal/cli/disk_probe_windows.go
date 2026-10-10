//go:build windows

package cli

import (
	"fmt"
	"syscall"
)

// diskReading is one platform's raw numbers for a filesystem, in bytes. It is
// deliberately two fields: the sizes and the block count are the platform's
// vocabulary, and the derived numbers (used, percent) belong to the caller that
// knows what it is reporting.
type diskReading struct {
	Total uint64
	Free  uint64
}

// diskUsage asks the Win32 API for the filesystem holding path, which is the
// spelling Windows has always had for the question and the one that needs no
// dependency. Same trade as doctor_probe_windows.go's GlobalMemoryStatusEx:
// the standard library has no cross-platform answer, and a fourth module in
// go.mod for two numbers in a report is not a trade AGENTS.md lets this package
// make.
//
// A path with no drive in it is refused by the call, so filepath.Abs("/") — the
// root of the current drive — is what the observe path hands in.
func diskUsage(path string) (diskReading, error) {
	var freeToCaller, total, freeBytes uint64
	if err := syscall.GetDiskFreeSpaceEx(syscall.StringToUTF16Ptr(path), &freeToCaller, &total, &freeBytes); err != nil {
		return diskReading{}, fmt.Errorf("measure the filesystem at %s: %w", path, err)
	}
	return diskReading{Total: total, Free: freeBytes}, nil
}
