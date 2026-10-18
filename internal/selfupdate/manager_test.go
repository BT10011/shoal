package selfupdate

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// machine fakes what detectManager looks at: the tools installed, and the
// one package manager, if any, that owns the binary.
func machine(owner string, tools ...string) probes {
	return probes{
		has: func(tool string) bool {
			for _, t := range tools {
				if t == tool {
					return true
				}
			}
			return false
		},
		owns: func(args ...string) bool { return args[0] == owner },
	}
}

func TestDetectManagerKnowsEachWayShoalIsInstalled(t *testing.T) {
	winget := `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\BT10011.Shoal_Microsoft.Winget.Source_8wekyb3d8bbwe\shoal.exe`
	for _, tc := range []struct {
		name, target, goos string
		p                  probes
		want, upgrade      string // manager name and upgrade command, "" for none
	}{
		{"Homebrew on Apple silicon", "/opt/homebrew/Cellar/shoal/2.0.0/bin/shoal", "darwin", machine(""), "Homebrew", "upgrade shoal"},
		{"Homebrew on Intel", "/usr/local/Cellar/shoal/2.0.0/bin/shoal", "darwin", machine(""), "Homebrew", "upgrade shoal"},
		{"Homebrew on Linux", "/home/linuxbrew/.linuxbrew/Cellar/shoal/2.0.0/bin/shoal", "linux", machine(""), "Homebrew", "upgrade shoal"},
		{"winget", winget, "windows", machine(""), "winget", "winget upgrade --id " + WingetID + " --exact"},
		{"a .deb with apt", systemBinary, "linux", machine("dpkg-query", "apt-get"), "apt", "apt-get install"},
		{"a .deb without apt", systemBinary, "linux", machine("dpkg-query"), "dpkg", "dpkg -i"},
		{"an .rpm with dnf", systemBinary, "linux", machine("rpm", "dnf", "yum"), "dnf", "dnf install"},
		{"an .rpm with zypper", systemBinary, "linux", machine("rpm", "zypper"), "zypper", "zypper install --allow-unsigned-rpm"},
		{"an .rpm alone", systemBinary, "linux", machine("rpm"), "rpm", "rpm -U"},
		{"an Arch package", systemBinary, "linux", machine("pacman"), "pacman", "pacman -U"},
		{"install.sh's place", "/usr/local/bin/shoal", "linux", machine("dpkg-query", "apt-get"), "", ""},
		{"a file no package owns", systemBinary, "linux", machine(""), "", ""},
		{"the zip on Windows", `C:\Tools\shoal.exe`, "windows", machine(""), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := detectManager(tc.target, tc.goos, "amd64", tc.p)
			switch {
			case tc.want == "" && m != nil:
				t.Fatalf("found %s, want none", m.name)
			case tc.want == "":
				return
			case m == nil:
				t.Fatalf("found none, want %s", tc.want)
			}
			if m.name != tc.want || !strings.HasSuffix(strings.Join(m.upgrade, " "), tc.upgrade) {
				t.Errorf("found %s upgrading with %q, want %s with %q", m.name, m.upgrade, tc.want, tc.upgrade)
			}
		})
	}
}

func TestSystemPackagesAreTheReleaseFileForThisMachine(t *testing.T) {
	for owner, want := range map[string]string{
		"dpkg-query": "shoal-linux-arm64.deb",
		"rpm":        "shoal-linux-arm64.rpm",
		"pacman":     "shoal-linux-arm64.pkg.tar.zst",
	} {
		m := detectManager(systemBinary, "linux", "arm64", machine(owner))
		if m == nil || m.pkg != want || !m.root {
			t.Errorf("owned by %s: %+v, want %s installed as root", owner, m, want)
		}
	}
}

func TestUpdateHandsAHomebrewInstallToBrew(t *testing.T) {
	srv := release(t, "v1.2.0", nil)
	defer srv.Close()
	var ran []string
	var out bytes.Buffer
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0",
		Target: filepath.Join(t.TempDir(), "Cellar", "shoal", "1.1.0", "bin", "shoal"),
		GOOS:   "darwin", GOARCH: "arm64", Out: &out,
		Run: func(name string, args ...string) error {
			ran = append([]string{name}, args...)
			return nil
		},
		Elevate: refuse,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "brew upgrade shoal"; strings.Join(ran, " ") != want {
		t.Errorf("ran %q, want %q", ran, want)
	}
	if !strings.Contains(out.String(), "installed with Homebrew") {
		t.Errorf("output does not say why brew does it:\n%s", out.String())
	}
}

func TestUpdateLeavesWingetsCommandToTheUser(t *testing.T) {
	srv := release(t, "v1.2.0", nil)
	defer srv.Close()
	var out bytes.Buffer
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0",
		Target: `C:\Users\someone\AppData\Local\Microsoft\WinGet\Packages\BT10011.Shoal_Microsoft.Winget.Source_8wekyb3d8bbwe\shoal.exe`,
		GOOS:   "windows", GOARCH: "amd64", Out: &out, Run: refuse, Elevate: refuse,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "winget upgrade --id "+WingetID+" --exact") {
		t.Errorf("output does not give winget's command:\n%s", out.String())
	}
}

// fakeTool puts a stand-in for a package manager on PATH: it records its
// arguments and keeps a copy of the package it was given, its last.
func fakeTool(t *testing.T, name string) (argsFile, pkgFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, pkgFile = filepath.Join(dir, "args"), filepath.Join(dir, "package")
	script := "#!/bin/sh\necho \"$*\" >" + argsFile + "\nfor last; do :; done\ncp \"$last\" " + pkgFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile, pkgFile
}

func TestUpdateInstallsTheNewDebThroughApt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the root step is a POSIX shell script")
	}
	deb := []byte("a new shoal package")
	srv := release(t, "v1.2.0", map[string][]byte{
		"shoal-linux-amd64.deb": deb,
		"SHA256SUMS":            sums(map[string][]byte{"shoal-linux-amd64.deb": deb}),
	})
	defer srv.Close()
	argsFile, pkgFile := fakeTool(t, "apt-get")
	m := machine("dpkg-query", "apt-get")
	var out bytes.Buffer
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0", Target: systemBinary,
		GOOS: "linux", GOARCH: "amd64", Out: &out, Has: m.has, Owns: m.owns, Run: refuse,
		// sudo, played by running the command as this user.
		Elevate: func(name string, args ...string) error {
			out, err := exec.Command(name, args...).CombinedOutput()
			if err != nil {
				t.Logf("%s", out)
			}
			return err
		},
	})
	if err != nil {
		t.Fatalf("Update = %v\n%s", err, out.String())
	}
	if got, _ := os.ReadFile(pkgFile); string(got) != string(deb) {
		t.Errorf("apt-get was given %q, want the downloaded package", got)
	}
	if args, _ := os.ReadFile(argsFile); !strings.HasPrefix(string(args), "install /") || !strings.Contains(string(args), "shoal-linux-amd64.deb") {
		t.Errorf("apt-get ran with %q, want install and the package's path", args)
	}
}

func TestUpdateRefusesAPackageThatDoesNotMatchItsChecksum(t *testing.T) {
	srv := release(t, "v1.2.0", map[string][]byte{
		"shoal-linux-amd64.rpm": []byte("tampered"),
		"SHA256SUMS":            sums(map[string][]byte{"shoal-linux-amd64.rpm": []byte("the real one")}),
	})
	defer srv.Close()
	m := machine("rpm", "dnf")
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0", Target: systemBinary,
		GOOS: "linux", GOARCH: "amd64", Has: m.has, Owns: m.owns, Run: refuse, Elevate: refuse,
	})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Update = %v, want a refusal naming the checksum", err)
	}
}

// With --yes the user has agreed in advance, so the package manager is
// told not to ask again; without it, the user answers the manager too.
func TestRemovalIsToldNotToAskOnlyWithYes(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		p            probes
		ask, yes     string
	}{
		{"apt", systemBinary, machine("dpkg-query", "apt-get"), "apt-get remove shoal", "apt-get remove -y shoal"},
		{"dpkg", systemBinary, machine("dpkg-query"), "dpkg -r shoal", "dpkg -r shoal"},
		{"dnf", systemBinary, machine("rpm", "dnf"), "dnf remove shoal", "dnf remove -y shoal"},
		{"zypper", systemBinary, machine("rpm", "zypper"), "zypper remove shoal", "zypper --non-interactive remove shoal"},
		{"yum", systemBinary, machine("rpm", "yum"), "yum remove shoal", "yum remove -y shoal"},
		{"rpm", systemBinary, machine("rpm"), "rpm -e shoal", "rpm -e shoal"},
		{"pacman", systemBinary, machine("pacman"), "pacman -R shoal", "pacman -R --noconfirm shoal"},
		{"Homebrew", "/opt/homebrew/Cellar/shoal/2.0.0/bin/shoal", machine(""), "uninstall shoal", "uninstall shoal"},
	} {
		goos := "linux"
		if strings.Contains(tc.target, "homebrew") {
			goos = "darwin"
		}
		m := detectManager(tc.target, goos, "amd64", tc.p)
		if m == nil {
			t.Fatalf("%s: no manager found", tc.name)
		}
		if got := strings.Join(m.removal(false), " "); !strings.HasSuffix(got, tc.ask) {
			t.Errorf("%s without --yes: %q, want %q", tc.name, got, tc.ask)
		}
		if got := strings.Join(m.removal(true), " "); !strings.HasSuffix(got, tc.yes) {
			t.Errorf("%s with --yes: %q, want %q", tc.name, got, tc.yes)
		}
	}
}

func TestUninstallHandsAHomebrewInstallToBrew(t *testing.T) {
	in := installation_(t)
	o := in.options()
	brewed := filepath.Join(t.TempDir(), "Cellar", "shoal", "2.0.0", "bin", "shoal")
	if err := os.MkdirAll(filepath.Dir(brewed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brewed, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ran []string
	var out bytes.Buffer
	o.Binary, o.Out, o.Confirm = brewed, &out, func(string) bool { return true }
	o.Run = func(name string, args ...string) error {
		ran = append([]string{name}, args...)
		return nil
	}
	if err := Uninstall(o); err != nil {
		t.Fatalf("Uninstall = %v\n%s", err, out.String())
	}
	if want := "brew uninstall shoal"; strings.Join(ran, " ") != want {
		t.Errorf("ran %q, want %q", ran, want)
	}
	if !exists(brewed) {
		t.Error("shoal deleted Homebrew's file itself rather than leaving it to brew")
	}
	for _, p := range []string{filepath.Dir(in.config), filepath.Dir(in.history)} {
		if exists(p) {
			t.Errorf("%s is still there", p)
		}
	}
}

func TestUninstallUndoesSetupAsRootAndSaysTheGroupStays(t *testing.T) {
	in := installation_(t)
	o := in.options()
	var ran []string
	var out bytes.Buffer
	o.Out, o.Confirm = &out, func(string) bool { return true }
	o.Undo = func() []string { return []string{"/bin/sh", "-c", "the undo"} }
	o.Elevate = func(name string, args ...string) error {
		ran = append([]string{name}, args...)
		return nil
	}
	if err := Uninstall(o); err != nil {
		t.Fatalf("Uninstall = %v\n%s", err, out.String())
	}
	if strings.Join(ran, " ") != "/bin/sh -c the undo" {
		t.Errorf("sudo ran %q, want setup's undo", ran)
	}
	if !strings.Contains(out.String(), "group stays") {
		t.Errorf("output does not say the group stays:\n%s", out.String())
	}
}

func TestRootInstallRefusesAPackageChangedAfterTheCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the root step is a POSIX shell script")
	}
	deb := []byte("a new shoal package")
	srv := release(t, "v1.2.0", map[string][]byte{
		"shoal-linux-amd64.deb": deb,
		"SHA256SUMS":            sums(map[string][]byte{"shoal-linux-amd64.deb": deb}),
	})
	defer srv.Close()
	argsFile, _ := fakeTool(t, "apt-get")
	m := machine("dpkg-query", "apt-get")
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0", Target: systemBinary,
		GOOS: "linux", GOARCH: "amd64", Has: m.has, Owns: m.owns, Run: refuse,
		Elevate: func(name string, args ...string) error {
			os.WriteFile(args[3], []byte("swapped in after the check"), 0o600)
			return exec.Command(name, args...).Run()
		},
	})
	if err == nil {
		t.Fatal("Update = nil, want a refusal")
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Error("apt-get was run with a package that no longer matched")
	}
}
