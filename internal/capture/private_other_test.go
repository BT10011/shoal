//go:build !windows

package capture

import (
	"fmt"
	"os"
)

// ownerOnlyProblem says why a file is readable by anyone but its owner, or
// returns "" when it is not: mode 0600.
func ownerOnlyProblem(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return err.Error()
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return fmt.Sprintf("mode %v, want 0600", perm)
	}
	return ""
}
