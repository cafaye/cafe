//go:build darwin

package cli

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// sysctlByName reads one uint64 sysctl by asking `sysctl`, because the standard
// library's syscall package has no uint64 getter for a name and adding a
// dependency for it would be a third module in go.mod for one number in a
// report. It is a subprocess, so it is the second thing in `cafe doctor` that
// runs a command after the container runtime probe, and both are bounded.
func sysctlByName(name string) (uint64, error) {
	out, err := exec.CommandContext(context.Background(), "sysctl", "-n", name).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
}

// totalMemoryBytes is the memory question in this platform's spelling.
func totalMemoryBytes() (uint64, error) { return sysctlByName("hw.memsize") }
