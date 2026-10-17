// Package elevate runs the few commands shoal needs the administrator for:
// granting itself raw network access, and putting an update somewhere only
// root can write. Each runs on its own through sudo, with the terminal
// attached so sudo can ask for the password, rather than shoal running as
// root itself.
package elevate

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// ErrUnavailable means there is no way to run a command as root from here:
// no sudo to ask through, or a platform where shoal never needs to.
var ErrUnavailable = errors.New("sudo is not available to run it as the administrator")

// Run runs a command as root, through sudo unless this process already is
// root. sudo asks for the password on the terminal, so the command's input
// and output are the terminal's too.
func Run(name string, args ...string) error {
	cmd, err := Command(name, args...)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Command is the command Run would start, for a caller that needs its
// input or output itself.
func Command(name string, args ...string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		return nil, ErrUnavailable
	}
	if IsRoot() {
		return exec.Command(name, args...), nil
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return nil, ErrUnavailable
	}
	return exec.Command(sudo, append([]string{name}, args...)...), nil
}

// Available reports whether Run can work here.
func Available() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	if IsRoot() {
		return true
	}
	_, err := exec.LookPath("sudo")
	return err == nil
}

// IsRoot reports whether this process runs as root.
func IsRoot() bool { return os.Geteuid() == 0 }
