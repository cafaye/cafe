//go:build linux

package cli

import (
	"strconv"
	"strings"
)

// sysctlByName exists so doctor.go compiles everywhere; Linux has no sysctl for
// this and the memory probe does not use it.
func sysctlByName(string) (uint64, error) { return 0, nil }

// totalMemoryBytes is the memory question in this platform's spelling. Linux
// names it `MemTotal` and states it in kibibytes, so the conversion belongs here
// rather than in the caller: a caller that has to know the unit is a caller that
// will eventually forget it.
func totalMemoryBytes() (uint64, error) {
	const kibibyte = 1024
	out, err := sysctlByName("MemTotal")
	if err != nil {
		return 0, err
	}
	return out * kibibyte, nil
}
