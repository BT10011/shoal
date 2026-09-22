package selfupdate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installation lays out what an installed shoal leaves on disk: the
// binary, a settings file, a history database and the PATH line the
// installer added to a shell's startup file.
type installation struct {
	binary, config, history, rc string
}

func installation_(t *testing.T) installation {
	t.Helper()
	root := t.TempDir()
	in := installation{
		binary:  filepath.Join(root, "bin", "shoal"),
		config:  filepath.Join(root, "config", "shoal", "config.toml"),
		history: filepath.Join(root, "data", "shoal", "history.db"),
		rc:      filepath.Join(root, ".zshrc"),
	}
	for _, p := range []string{in.binary, in.config, in.history} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rc := "export EDITOR=vi\nexport PATH=\"$HOME/mine:$PATH\"\n\n" +
		installerMarker + "\nexport PATH=\"" + filepath.Dir(in.binary) + ":$PATH\"\n"
	if err := os.WriteFile(in.rc, []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	return in
}

func (in installation) options() UninstallOptions {
	return UninstallOptions{
		Binary: in.binary, ConfigFile: in.config, HistoryFile: in.history,
		Shells: []string{in.rc},
	}
}

func TestUninstallRemovesEverythingShoalWrote(t *testing.T) {
	in := installation_(t)
	o := in.options()
	var out bytes.Buffer
	o.Out, o.Confirm = &out, func(string) bool { return true }

	if err := Uninstall(o); err != nil {
		t.Fatalf("Uninstall = %v, want nil", err)
	}
	for _, p := range []string{in.binary, filepath.Dir(in.config), filepath.Dir(in.history)} {
		if exists(p) {
			t.Errorf("%s survived the uninstall", p)
		}
	}
	rc, err := os.ReadFile(in.rc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rc), installerMarker) || strings.Contains(string(rc), "bin:$PATH") {
		t.Errorf("the installer's PATH line survived:\n%s", rc)
	}
	// The user's own lines are not the installer's business.
	for _, want := range []string{"export EDITOR=vi", `export PATH="$HOME/mine:$PATH"`} {
		if !strings.Contains(string(rc), want) {
			t.Errorf("uninstall removed a line it did not add: %q missing from\n%s", want, rc)
		}
	}
}

func TestUninstallRemovesNothingWithoutAgreement(t *testing.T) {
	in := installation_(t)
	o := in.options()
	var out bytes.Buffer
	o.Out, o.Confirm = &out, func(string) bool { return false }

	if err := Uninstall(o); err != nil {
		t.Fatalf("Uninstall = %v, want nil", err)
	}
	for _, p := range []string{in.binary, in.config, in.history} {
		if !exists(p) {
			t.Errorf("%s was removed despite the answer being no", p)
		}
	}
	if !strings.Contains(out.String(), "Nothing was removed") {
		t.Errorf("output = %q, want it to say nothing was removed", out.String())
	}
}

func TestUninstallRemovesNothingWithNobodyToAsk(t *testing.T) {
	// A nil Confirm is a script or a pipe. Removing a program and every
	// network it remembers is not something to do unasked.
	in := installation_(t)
	if err := Uninstall(in.options()); err != nil {
		t.Fatalf("Uninstall = %v, want nil", err)
	}
	if !exists(in.binary) {
		t.Error("the binary was removed with nobody to agree to it")
	}
}

func TestPlanListsWhatItWillRemoveAndSkipsWhatIsNotThere(t *testing.T) {
	in := installation_(t)
	if err := os.RemoveAll(filepath.Dir(in.history)); err != nil {
		t.Fatal(err)
	}
	items, err := Plan(in.options())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range items {
		paths = append(paths, it.Path)
		if it.What == "" {
			t.Errorf("%s is listed with no explanation of what it is", it.Path)
		}
	}
	joined := strings.Join(paths, " ")
	if strings.Contains(joined, "history") {
		t.Errorf("a history file that is not there should not be listed: %v", paths)
	}
	for _, want := range []string{in.binary, filepath.Dir(in.config), in.rc} {
		if !strings.Contains(joined, want) {
			t.Errorf("Plan is missing %s: %v", want, paths)
		}
	}
}

func TestPlanListsOneDirectoryOnceWhenBothFilesShareIt(t *testing.T) {
	// macOS keeps settings and history in the same per-user directory, so
	// the two paths resolve to one thing.
	root := t.TempDir()
	dir := filepath.Join(root, "Application Support", "shoal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "shoal")
	for _, p := range []string{bin, filepath.Join(dir, "config.toml"), filepath.Join(dir, "history.db")} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	items, err := Plan(UninstallOptions{
		Binary:      bin,
		ConfigFile:  filepath.Join(dir, "config.toml"),
		HistoryFile: filepath.Join(dir, "history.db"),
		Shells:      []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, it := range items {
		if it.Path == dir {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the shared directory is listed %d times, want once: %+v", seen, items)
	}
}

func TestUninstallLeavesAHistoryFileSomewhereElseAsAFile(t *testing.T) {
	// --history PATH puts the database outside a directory shoal owns, so
	// only the file may be removed, never the folder holding it.
	root := t.TempDir()
	bin := filepath.Join(root, "shoal")
	elsewhere := filepath.Join(root, "notes", "scan.db")
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{bin, elsewhere, filepath.Join(root, "notes", "keep.txt")} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o := UninstallOptions{Binary: bin, HistoryFile: elsewhere, Shells: []string{},
		Confirm: func(string) bool { return true }}
	if err := Uninstall(o); err != nil {
		t.Fatalf("Uninstall = %v, want nil", err)
	}
	if exists(elsewhere) {
		t.Error("the named history file survived")
	}
	if !exists(filepath.Join(root, "notes", "keep.txt")) {
		t.Error("uninstall removed a folder it did not own")
	}
}

func TestRemoveInstallerLineLeavesAFileWithoutOneAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".bashrc")
	original := "export PATH=\"/usr/local/bin:$PATH\"\nalias ll='ls -l'\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := installerLine(path); ok {
		t.Fatal("found an installer line in a file that has none")
	}
}
