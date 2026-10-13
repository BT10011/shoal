//go:build !windows

package selfupdate

import "os"

// removeRunning deletes the program this process is running from, which
// Unix allows: the running process keeps its copy until it exits.
func removeRunning(path string) (later bool, err error) {
	return false, os.RemoveAll(path)
}
