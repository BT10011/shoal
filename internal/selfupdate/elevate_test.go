//go:build !windows

package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BT10011/shoal/internal/access"
	"github.com/BT10011/shoal/internal/elevate"
)

// rootless stands in for sudo: it runs what an update asks root to run as
// this user, with the read-only folder opened up while it does, which is
// how root would find it. It records each command, and before can tamper
// with things first.
type rootless struct {
	dir    string
	calls  [][]string
	before func(args []string)
}

func (r *rootless) run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.before != nil {
		r.before(args)
	}
	if err := os.Chmod(r.dir, 0o755); err != nil {
		return err
	}
	defer os.Chmod(r.dir, 0o555)
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// rootOwned writes a stand-in binary into a folder this user cannot write
// to, as /usr/local/bin is to anyone but root.
func rootOwned(t *testing.T, content string) (dir, target string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can write to any folder, so there is nothing to go through sudo for")
	}
	dir = filepath.Join(t.TempDir(), "bin")
	target = filepath.Join(dir, "shoal")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	return dir, target
}

func latestRelease(t *testing.T, binary string) string {
	t.Helper()
	asset := Asset("linux", "amd64")
	archive := archiveOf(t, "v1.2.0", "linux", "amd64", []byte(binary))
	srv := release(t, "v1.2.0", map[string][]byte{
		asset:        archive,
		"SHA256SUMS": sums(map[string][]byte{asset: archive}),
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/releases"
}

func TestUpdateGoesThroughSudoWhenTheFolderBelongsToRoot(t *testing.T) {
	dir, target := rootOwned(t, "the old shoal")
	sudo := &rootless{dir: dir}
	var out bytes.Buffer
	err := Update(context.Background(), Options{
		BaseURL: latestRelease(t, "the new shoal"), Version: "v1.1.0", Target: target,
		GOOS: "linux", GOARCH: "amd64", Out: &out, Elevate: sudo.run,
	})
	if err != nil {
		t.Fatalf("Update = %v, want nil\n%s", err, out.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "the new shoal" {
		t.Errorf("binary = %q, want the downloaded one", got)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v (err %v), want the old binary's 0755", fi.Mode().Perm(), err)
	}
	if len(sudo.calls) != 1 || sudo.calls[0][0] != "/bin/sh" {
		t.Errorf("sudo ran %q, want one /bin/sh", sudo.calls)
	}
	if !strings.Contains(out.String(), "belongs to the administrator") {
		t.Errorf("output does not say why sudo asks:\n%s", out.String())
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("the folder holds %d files, want only shoal", len(left))
	}
}

func TestRootCopyRefusesAFileChangedAfterTheCheck(t *testing.T) {
	// Between the check, made as the user, and root's copy, something the
	// user runs could rewrite the temporary file. Root must install only
	// what was checked.
	dir, target := rootOwned(t, "the old shoal")
	sudo := &rootless{dir: dir, before: func(args []string) {
		os.WriteFile(args[3], []byte("swapped in after the check"), 0o600)
	}}
	err := Update(context.Background(), Options{
		BaseURL: latestRelease(t, "the new shoal"), Version: "v1.1.0", Target: target,
		GOOS: "linux", GOARCH: "amd64", Elevate: sudo.run,
	})
	if err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("Update = %v, want a refusal", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "the old shoal" {
		t.Errorf("binary = %q, want the old one left untouched", got)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("the folder holds %d files, want only shoal", len(left))
	}
}

func TestUpdateWithNoSudoSaysWhatToRun(t *testing.T) {
	_, target := rootOwned(t, "the old shoal")
	err := Update(context.Background(), Options{
		BaseURL: latestRelease(t, "the new shoal"), Version: "v1.1.0", Target: target,
		GOOS: "linux", GOARCH: "amd64",
		Elevate: func(string, ...string) error { return elevate.ErrUnavailable },
	})
	if err == nil || !strings.Contains(err.Error(), "sudo shoal --upgrade") {
		t.Fatalf("Update = %v, want it to say what to run", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "the old shoal" {
		t.Errorf("binary = %q, want the old one left untouched", got)
	}
}

func TestPutGrantsTheCapabilityAgainInTheSameSudo(t *testing.T) {
	setcap := access.SetcapPath()
	if setcap == "" {
		t.Skip("no setcap here (it is Linux-only)")
	}
	_, target := rootOwned(t, "the old shoal")
	var calls [][]string
	o := Options{Elevate: func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}}
	if err := o.put(target, []byte("the new shoal"), true); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("sudo ran %d times, want once", len(calls))
	}
	if last := calls[0][len(calls[0])-1]; last != setcap {
		t.Errorf("rootCopy's setcap argument = %q, want %q", last, setcap)
	}
}

func TestRestoreRawAccessAsksSudoForSetcap(t *testing.T) {
	setcap := access.SetcapPath()
	if setcap == "" {
		t.Skip("no setcap here (it is Linux-only)")
	}
	target := installed(t, "the new shoal")
	var got []string
	var out bytes.Buffer
	o := Options{Out: &out, Elevate: func(name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}}
	o.restoreRawAccess(target)
	want := []string{setcap, "cap_net_raw+ep", target}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("sudo ran %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "granted again") {
		t.Errorf("output = %q, want it to say the capability is back", out.String())
	}
}
