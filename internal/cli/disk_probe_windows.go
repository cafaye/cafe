//go:build windows

package cli

import (
	"fmt"
	"syscall"
	"unsafe"
)

// diskReading is one platform's raw numbers for a filesystem, in bytes. It is
// deliberately two fields: the sizes and the block count are the platform's
// vocabulary, and the derived numbers (used, percent) belong to the caller that
// knows what it is reporting.
type diskReading struct {
	Total uint64
	Free  uint64
}

// getDiskFreeSpaceExW is the Win32 call, reached through the same lazy-proc
// route doctor_probe_windows.go takes to GlobalMemoryStatusEx. The standard
// library's own syscall package does not wrap this one, and the one it does wrap
// (GetDiskFreeSpace) answers in clusters and sectors — a caller that has to
// multiply its way to bytes is a caller that will eventually get the unit wrong.
//
// The four arguments are what the API takes: the directory, the bytes free to
// the caller, the volume's size, and the bytes free on the volume — out
// parameters, passed by pointer, which is why the call passes uintptrs built
// from their addresses.
var getDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// diskUsage asks the Win32 API for the filesystem holding path. Same trade as
// the memory probe: the standard library has no cross-platform answer, and a
// fourth module in go.mod for two numbers in a report is not a trade AGENTS.md
// lets this package make.
//
// A path with no drive in it is refused by the call, so filepath.Abs("/") — the
// root of the current drive — is what the observe path hands in.
func diskUsage(path string) (diskReading, error) {
	var freeToCaller, total, freeBytes uint64
	root, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return diskReading{}, fmt.Errorf("measure the filesystem at %s: %w", path, err)
	}
	ok, _, callErr := getDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(root)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&freeBytes)),
	)
	if ok == 0 {
		return diskReading{}, fmt.Errorf("measure the filesystem at %s: %w", path, callErr)
	}
	// total is what the volume holds; freeBytes is the unprivileged number. A box
	// runs as root, but the capacity a report wants is the one it can actually
	// use, and freeToCaller is that on every Windows volume that has quotas.
	return diskReading{Total: total, Free: freeBytes}, nil
}
