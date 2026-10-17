//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restart replaces this process with a fresh run of the same program and
// arguments, marked as restarted by setup. A capability granted to the
// file applies only from the next exec, and a new group from a new
// process, so this is how setup's change takes effect without the user
// typing the command again.
func restart() error {
	exe, err := programPath()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, append(os.Environ(), setupRestarted+"=1"))
}
