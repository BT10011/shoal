//go:build windows

package selfupdate

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A copy of this test binary, started with SHOAL_TEST_SLEEP set, is the
// running program the tests below replace and delete.
func TestHelperProcessSleeps(t *testing.T) {
	if os.Getenv("SHOAL_TEST_SLEEP") == "" {
		t.Skip("only runs as the helper process")
	}
	time.Sleep(3 * time.Second)
	os.Exit(0)
}

// runningCopy copies this test binary to dir as shoal.exe and starts it,
// sleeping, so that the file is a program Windows is running.
func runningCopy(t *testing.T, dir string) (string, *exec.Cmd) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "shoal.exe")
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
	cmd := exec.Command(target, "-test.run=^TestHelperProcessSleeps$")
	cmd.Env = append(os.Environ(), "SHOAL_TEST_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Prove Windows really refuses the plain way while it runs.
	if err := os.Remove(target); err == nil {
		t.Fatal("Windows deleted a running program; this test proves nothing")
	}
	return target, cmd
}

func TestUpdateReplacesARunningProgram(t *testing.T) {
	dir := t.TempDir()
	target, cmd := runningCopy(t, dir)
	if err := replace(target, []byte("the new shoal.exe")); err != nil {
		t.Fatalf("replacing a running program: %v", err)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, []byte("the new shoal.exe")) {
		t.Error("the new program is not in place")
	}
	if !exists(target + ".old") {
		t.Fatal("the running program was not moved aside to shoal.exe.old")
	}
	// While it runs, the leftover cannot go; once it has stopped, it can.
	removeLeftovers(target)
	if !exists(target + ".old") {
		t.Error("a program still running was deleted")
	}
	cmd.Wait()
	removeLeftovers(target)
	if exists(target + ".old") {
		t.Error("the leftover was not deleted once its program had stopped")
	}
}

func TestUninstallDeletesTheRunningProgramOnceItHasExited(t *testing.T) {
	dir := t.TempDir()
	target, cmd := runningCopy(t, dir)
	if err := removeAfterExit(target, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if !exists(target) {
		t.Fatal("deleted before its program had exited")
	}
	cmd.Wait()
	for end := time.Now().Add(15 * time.Second); exists(target) && time.Now().Before(end); {
		time.Sleep(100 * time.Millisecond)
	}
	if exists(target) {
		t.Error("the program was not deleted after it exited")
	}
}

func TestUninstallListsAnUpdatesLeftoverOnWindows(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "shoal.exe")
	for _, p := range []string{bin, bin + ".old"} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	items, err := Plan(UninstallOptions{Binary: bin, Shells: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, it := range items {
		listed = append(listed, filepath.Base(it.Path))
		if it.Running {
			t.Errorf("%s is marked running, but an uninstall given a binary is not running from it", it.Path)
		}
	}
	if got := strings.Join(listed, " "); got != "shoal.exe shoal.exe.old" {
		t.Errorf("Plan lists %q, want the program and the copy an update left", got)
	}
}
