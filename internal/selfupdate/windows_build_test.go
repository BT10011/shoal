package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// zipOf packs a Windows build the way make release will: shoal.exe in a
// folder named for the build.
func zipOf(t *testing.T, tag, goarch string, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	folder := fmt.Sprintf("shoal-%s-windows-%s", tag, goarch)
	for _, f := range []struct {
		name string
		body []byte
	}{
		{folder + "/README.md", []byte("readme")},
		{folder + "/shoal", []byte("not this one: the Windows program is shoal.exe")},
		{folder + "/shoal.exe", binary},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWindowsBuildsComeAsAZipHoldingShoalExe(t *testing.T) {
	if got := Asset("windows", "amd64"); got != "shoal-windows-amd64.zip" {
		t.Errorf("Asset(windows) = %q, want the .zip", got)
	}
	if got := Asset("linux", "arm64"); got != "shoal-linux-arm64.tar.gz" {
		t.Errorf("Asset(linux) = %q: v1 binaries ask for this name, so it must not change", got)
	}

	asset := Asset("windows", "amd64")
	archive := zipOf(t, "v2.0.0", "amd64", []byte("the new shoal.exe"))
	srv := release(t, "v2.0.0", map[string][]byte{
		asset:        archive,
		"SHA256SUMS": sums(map[string][]byte{asset: archive}),
	})
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "shoal.exe")
	if err := os.WriteFile(target, []byte("the old shoal.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0", Target: target,
		GOOS: "windows", GOARCH: "amd64", Out: &out,
	})
	if err != nil {
		t.Fatalf("Update = %v\n%s", err, out.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "the new shoal.exe" {
		t.Errorf("program = %q, want shoal.exe out of the zip", got)
	}
}

func TestSwapAsideKeepsTheOldProgramAndUndoesAFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shoal.exe")
	next := filepath.Join(dir, ".shoal-update-1")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		b, _ := os.ReadFile(p)
		return string(b)
	}

	write(target, "v1")
	write(target+".old", "v0, left by an update before last")
	write(next, "v2")
	if err := swapAside(next, target); err != nil {
		t.Fatal(err)
	}
	if read(target) != "v2" || read(target+".old") != "v1" || exists(next) {
		t.Errorf("after the swap: program %q, .old %q, new file still there %v; want v2, v1, false",
			read(target), read(target+".old"), exists(next))
	}

	// The new program never arrives: the old one goes back where it was.
	if err := swapAside(filepath.Join(dir, "missing"), target); err == nil {
		t.Fatal("a swap with nothing to put in place succeeded")
	}
	if read(target) != "v2" {
		t.Errorf("after a failed swap the program is %q, want v2 put back", read(target))
	}
}

func TestLeftoversAreOnlyTheCopiesAnUpdateMakes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shoal.exe")
	for _, name := range []string{"shoal.exe.old", "shoal.exe.old-2", "shoal.exe.old-17", "shoal.exe.old-backup", "shoal.exe.older", "shoal.exe.old-1"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for _, p := range leftovers(target) {
		got[filepath.Base(p)] = true
	}
	for _, want := range []string{"shoal.exe.old", "shoal.exe.old-2", "shoal.exe.old-17"} {
		if !got[want] {
			t.Errorf("%s is a copy an update made and was not found", want)
		}
	}
	for _, mine := range []string{"shoal.exe.old-backup", "shoal.exe.older", "shoal.exe.old-1"} {
		if got[mine] {
			t.Errorf("%s was taken for an update's leftover; it could be the user's", mine)
		}
	}
	removeLeftovers(target)
	if exists(filepath.Join(dir, "shoal.exe.old")) || !exists(filepath.Join(dir, "shoal.exe.old-backup")) {
		t.Error("removeLeftovers must delete the update's copies and nothing else")
	}
}
