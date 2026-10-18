//go:build !windows

package access

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The macOS setup runs as root on a Mac, which these tests cannot do. What
// they can do anywhere with a POSIX sh: check every script parses, that
// the files it writes are exactly the ones meant, and run the parts whose
// logic could be wrong against stand-ins.

func TestMacScriptsParse(t *testing.T) {
	for name, script := range map[string]string{
		"macSetup": macSetup, "macRemove": macRemove, "chmodScript": chmodScript, "giveBack": giveBack, "macGiveBack": macGiveBack,
	} {
		if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, out)
		}
	}
}

func TestMacSetupWritesTheScriptAndJobExactly(t *testing.T) {
	// Each file is written from a quoted here-document, so it lands as
	// written, with nothing expanded.
	for _, want := range []string{
		"<<'SHOAL_SCRIPT'\n" + chmodScript + "SHOAL_SCRIPT\n",
		"<<'SHOAL_PLIST'\n" + daemonPlist + "SHOAL_PLIST\n",
	} {
		if !strings.Contains(macSetup, want) {
			t.Errorf("macSetup does not write this verbatim:\n%s", want)
		}
	}
}

func TestDaemonPlistIsWellFormedAndRunsTheScript(t *testing.T) {
	d := xml.NewDecoder(strings.NewReader(daemonPlist))
	d.Strict = true
	var strs []string
	for {
		tok, err := d.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("daemonPlist is not well-formed XML: %v", err)
		}
		if cd, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(cd)) != "" {
			strs = append(strs, strings.TrimSpace(string(cd)))
		}
	}
	got := strings.Join(strs, " ")
	for _, want := range []string{"Label " + DaemonLabel, "ProgramArguments " + ChmodScript, "RunAtLoad"} {
		if !strings.Contains(got, want) {
			t.Errorf("daemonPlist is missing %q: %s", want, got)
		}
	}
}

func TestFreeGIDTakesTheLowestUnusedFrom100(t *testing.T) {
	for _, tc := range []struct {
		groups, want string
	}{
		{"wheel 0\nstaff 20\n", "100"},
		{"a 100\nb 101\nc 103\n", "102"},
		{"a 101\nb 100\nc 100\n", "102"}, // unsorted, and a shared ID
		{"nobody -2\na 99\nb 100\n", "101"},
	} {
		out, err := exec.Command("sh", "-c", "printf '"+tc.groups+"' | "+freeGID).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != tc.want {
			t.Errorf("free ID among %q = %s, want %s", tc.groups, got, tc.want)
		}
	}
}

func TestOpenDevicesSkipsMissingDevicesAndChangesTheRest(t *testing.T) {
	// Run under set -e, as in macSetup, with the device folder, sysctl,
	// chgrp and chmod replaced: a device that cannot be opened must not
	// stop the script, and every device that exists is changed.
	dir := t.TempDir()
	devs := filepath.Join(dir, "dev")
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{devs, bin} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"bpf0", "bpf1"} {
		if err := os.WriteFile(filepath.Join(devs, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "log")
	for name, body := range map[string]string{
		"sysctl": "echo 4",
		"chgrp":  `echo "chgrp $*" >>"` + log + `"`,
		"chmod":  `echo "chmod $*" >>"` + log + `"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "set -e\n" + strings.ReplaceAll(openDevices, "/dev/bpf", devs+"/bpf")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openDevices stopped: %v\n%s", err, out)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	b0, b1 := filepath.Join(devs, "bpf0"), filepath.Join(devs, "bpf1")
	want := "chgrp " + Group + " " + b0 + " " + b1 + "\nchmod g+rw " + b0 + " " + b1 + "\n"
	if string(got) != want {
		t.Errorf("openDevices ran:\n%s\nwant:\n%s", got, want)
	}
}

// runGiveBack runs give_back for the account alice, whose home is home,
// with id and chown replaced and holds made in a folder of the test's
// rather than /private/tmp. The test runs as an ordinary account, so a
// folder it owns stands in for one root owns. It returns what chown was
// asked to do, one call to a line.
func runGiveBack(t *testing.T, home string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	holds := filepath.Join(dir, "tmp")
	for _, d := range []string{bin, holds} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "log")
	for name, body := range map[string]string{
		"id":    `echo "alice:*:501:20::0:0:Alice:` + home + `:/bin/zsh"`,
		"chown": `echo "chown $*" >>"` + log + `"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("/private/tmp", holds, "-user root", "-user "+strconv.Itoa(os.Getuid())).Replace(giveBack)
	cmd := exec.Command("sh", "-c", "set -e\nuser=alice\n"+script+"give_back\n")
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("give_back failed: %v\n%s", err, out)
	}
	if left, _ := os.ReadDir(holds); len(left) != 0 {
		t.Errorf("give_back left %d holding folders behind", len(left))
	}
	got, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(got)
}

// shoalData makes the folder of settings and history under home.
func shoalData(t *testing.T, home string, mode os.FileMode) string {
	t.Helper()
	data := filepath.Join(home, DataFolder)
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"history.db", "config.toml"} {
		if err := os.WriteFile(filepath.Join(data, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(data, mode); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGiveBackChangesTheContentsThenTheFolderAndPutsItBack(t *testing.T) {
	home := filepath.Join(t.TempDir(), "alice")
	data := shoalData(t, home, 0o700)
	got := runGiveBack(t, home)
	calls := strings.Split(strings.TrimSpace(got), "\n")
	if len(calls) != 1 {
		t.Fatalf("chown was called %d times, want once for everything:\n%s", len(calls), got)
	}
	args := strings.Fields(calls[0])
	if len(args) != 6 || args[1] != "-h" || args[2] != "alice" {
		t.Fatalf("chown %v, want chown -h alice and the folder's three paths", args[1:])
	}
	// What is changed is the copy held where only root can reach, never
	// the path in the home folder, and the folder itself goes last.
	held := filepath.Dir(filepath.Dir(args[3]))
	if !strings.HasSuffix(args[5], "/shoal") || filepath.Dir(args[5]) != held {
		t.Errorf("the folder was not changed last, or not in its holding folder: %v", args[3:])
	}
	files := []string{filepath.Base(args[3]), filepath.Base(args[4])}
	sort.Strings(files)
	if files[0] != "config.toml" || files[1] != "history.db" {
		t.Errorf("chown changed %v, want the settings and the history", files)
	}
	for _, f := range []string{"history.db", "config.toml"} {
		if b, err := os.ReadFile(filepath.Join(data, f)); err != nil || string(b) != f {
			t.Errorf("%s did not come back intact: %q, %v", f, b, err)
		}
	}
}

func TestGiveBackLeavesAFolderOthersCouldWriteTo(t *testing.T) {
	// Someone else could have put anything in it, a hard link to a system
	// file among them, so root changes nothing in it.
	home := filepath.Join(t.TempDir(), "alice")
	data := shoalData(t, home, 0o770)
	if got := runGiveBack(t, home); got != "" {
		t.Errorf("give_back changed a folder others could write to:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(data, "history.db")); err != nil {
		t.Errorf("the folder did not come back: %v", err)
	}
}

func TestGiveBackLeavesALinkAndAMissingFolderAlone(t *testing.T) {
	home := filepath.Join(t.TempDir(), "alice")
	if got := runGiveBack(t, home); got != "" {
		t.Errorf("give_back changed something with no folder there:\n%s", got)
	}
	elsewhere := shoalData(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
	link := filepath.Join(home, DataFolder)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if got := runGiveBack(t, home); got != "" {
		t.Errorf("give_back followed a link:\n%s", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was not left in place: %v", err)
	}
}

func TestMacSetupGivesBackOnlyWhenAsked(t *testing.T) {
	if !strings.Contains(macSetup, "if [ \"$2\" = 1 ]; then\n\tgive_back || ") {
		t.Error("macSetup does not give the folder back when $2 is 1")
	}
}

func TestMacGiveBackOnlyGivesBackAndFailsLoudly(t *testing.T) {
	if !strings.Contains(macGiveBack, giveBack+"give_back || {") || !strings.Contains(macGiveBack, "exit 1") {
		t.Error("macGiveBack should run give_back and stop with an error when it fails")
	}
	for _, other := range []string{"dseditgroup", "launchctl", "chgrp"} {
		if strings.Contains(macGiveBack, other) {
			t.Errorf("macGiveBack should change nothing but the folder, yet runs %s", other)
		}
	}
}

func TestDescribeOpensWithAnIntroWhenThereIsOne(t *testing.T) {
	s := Setup{Intro: "Your settings belong to root.", Changes: []string{"give them back"}}
	got := s.Describe()
	if !strings.HasPrefix(got, "Your settings belong to root.\n") || strings.Contains(got, "ARP") {
		t.Errorf("an Intro replaces the sentence about access:\n%s", got)
	}
	if !strings.HasPrefix((Setup{Missing: "x"}).Describe(), "Shoal sweeps the network with ARP") {
		t.Error("without an Intro the description starts with access")
	}
}

// runMacGiveBack runs macGiveBack, the whole script, for alice as root
// would, with the same stand-ins as runGiveBack, holding folders made in
// holds. It returns the script's output and error.
func runMacGiveBack(t *testing.T, home, holds string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	for name, body := range map[string]string{
		"id":    `echo "alice:*:501:20::0:0:Alice:` + home + `:/bin/zsh"`,
		"chown": `echo "chown $*" >>"` + log + `"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("/private/tmp", holds, "-user root", "-user "+strconv.Itoa(os.Getuid()),
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "PATH="+bin+":/usr/bin:/bin").Replace(macGiveBack)
	out, err := exec.Command("sh", "-c", script, "shoal-setup", "alice").CombinedOutput()
	chowns, _ := os.ReadFile(log)
	return string(out), string(chowns), err
}

func TestMacGiveBackHandsTheFolderToTheUserNamed(t *testing.T) {
	home := t.TempDir()
	data := shoalData(t, home, 0o700)
	holds := t.TempDir()
	out, chowns, err := runMacGiveBack(t, home, holds)
	if err != nil {
		t.Fatalf("macGiveBack failed: %v\n%s", err, out)
	}
	if !strings.Contains(chowns, "chown -h alice "+filepath.Join(holds)) || func() bool { _, err := os.Stat(filepath.Join(data, "history.db")); return err != nil }() {
		t.Errorf("the folder should be handed to alice and put back where it was:\n%s", chowns)
	}
}

func TestMacGiveBackStopsWithAnErrorWhenItCannot(t *testing.T) {
	home := t.TempDir()
	shoalData(t, home, 0o700)
	out, _, err := runMacGiveBack(t, home, filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(out, "could not give alice back") || !strings.Contains(out, "sudo chown -R alice") {
		t.Errorf("a failure should stop the script and say what to run instead (err %v):\n%s", err, out)
	}
}
