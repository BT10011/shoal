package main

import (
	"errors"
	"os"
	"os/exec"
)

// restart runs the same program and arguments again, marked as restarted
// by setup, and exits with its status. Windows cannot replace a process in
// place, and the fresh one is needed: this one has already looked for
// Npcap and remembers not finding it.
func restart() error {
	exe, err := programPath()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), setupRestarted+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
