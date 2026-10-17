//go:build !windows

package access

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The macOS setup runs as root on a Mac, which these tests cannot do. What
// they can do anywhere with a POSIX sh: check every script parses, that
// the files it writes are exactly the ones meant, and run the parts whose
// logic could be wrong against stand-ins.

func TestMacScriptsParse(t *testing.T) {
	for name, script := range map[string]string{
		"macSetup": macSetup, "macRemove": macRemove, "chmodScript": chmodScript,
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
