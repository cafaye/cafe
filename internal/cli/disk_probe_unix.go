//go:build linux || darwin

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

// diskUsage measures the filesystem holding path. Linux states the block size
// as an int64 and darwin as a uint32, which is the whole reason this file is
// split from its caller: the conversion belongs where the platform's unit is
// known rather than in a caller that has to remember which spelling applies.
//
// `syscall` here is the same trade `cafe doctor`'s memory probe makes and the
// package doc's own comment records: the standard library has no cross-platform
// way to ask for a filesystem's size, and a fourth module in go.mod for two
// numbers in a report is not a trade AGENTS.md lets this package make.
func diskUsage(path string) (diskReading, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return diskReading{}, fmt.Errorf("measure the filesystem at %s: %w", path, err)
	}
	bsize := uint64(st.Bsize)
	// Bavail is what an unprivileged box can write; Blocks is the whole device.
	// A box runs as root, but the number a capacity report wants is the one the
	// box can actually use, and the difference between the two is the filesystem's
	// reserved blocks — which exist precisely so a box filling up does not stop
	// root from logging in.
	return diskReading{
		// The double conversion is the portable spelling: on linux these
		// fields are already 64 bits and on darwin they are 32, so a single
		// uint64() is either unnecessary (and flagged) or missing (and does
		// not compile). int64 is the width both platforms reach.
		Total: bsize * uint64(int64(st.Blocks)),
		Free:  bsize * uint64(int64(st.Bavail)),
	}, nil
}
