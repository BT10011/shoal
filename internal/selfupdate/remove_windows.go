//go:build windows

package selfupdate

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// removeRunning deletes the program this process is running from. Windows
// will not delete a running program, so a hidden PowerShell is started that
// waits for this process to end and then deletes the file; it reports that
// the removal happens later.
func removeRunning(path string) (later bool, err error) {
	return true, removeAfterExit(path, os.Getpid())
}

// removeAfterExit starts Windows' own PowerShell, hidden and in its own
// process group (so Ctrl+C in this terminal does not stop it), to wait for
// process pid to end and then delete path, trying for a few seconds in case
// the file is still held a moment after the process has gone. The script
// goes as -EncodedCommand, so no path can break its quoting.
func removeAfterExit(path string, pid int) error {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	// By full path: a powershell.exe earlier on the PATH is not ours.
	ps := filepath.Join(sys, "WindowsPowerShell", "v1.0", "powershell.exe")
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	script := fmt.Sprintf("Wait-Process -Id %d -ErrorAction SilentlyContinue; "+
		"for ($i = 0; $i -lt 20 -and (Test-Path -LiteralPath %s); $i++) { "+
		"Remove-Item -LiteralPath %s -Force -ErrorAction SilentlyContinue; "+
		"if (Test-Path -LiteralPath %s) { Start-Sleep -Milliseconds 250 } }",
		pid, quoted, quoted, quoted)
	start := func(flags uint32) (*exec.Cmd, error) {
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encodeCommand(script))
		cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP | flags}
		return cmd, cmd.Start()
	}
	// Out of this session's job if it is allowed: Windows' SSH server ends
	// every process in a session's job when the session closes, which
	// would end this one before shoal had exited. A job that does not
	// allow it refuses the start, and then it runs inside the job.
	cmd, err := start(windows.CREATE_BREAKAWAY_FROM_JOB)
	if err != nil {
		cmd, err = start(0)
	}
	if err != nil {
		return fmt.Errorf("cannot start PowerShell to delete it once shoal has exited: %w", err)
	}
	return cmd.Process.Release()
}

// encodeCommand is PowerShell's -EncodedCommand form: the script as
// UTF-16LE, in base64.
func encodeCommand(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}
