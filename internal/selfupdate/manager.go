package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// What the packages call shoal. The Homebrew formula lives in the tap
// BT10011/homebrew-tap, but brew is given only its short name: brew finds
// an installed formula by that name and upgrades it from the tap it came
// from, so the command still works if shoal moves to homebrew-core, where
// the tap's full name would not. The winget package is in
// microsoft/winget-pkgs; the .deb, .rpm and Arch packages, attached to
// every release, install the program as systemBinary.
const (
	HomebrewFormula = "shoal"
	WingetID        = "BT10011.Shoal"
	systemBinary    = "/usr/bin/shoal"
)

// manager is the package manager that installed shoal, when one did. An
// update or a removal is then its job: it owns the files, and its records
// would be wrong if shoal changed them behind its back.
type manager struct {
	name string
	// upgrade and remove are its commands. For a system package, upgrade's
	// last argument is the downloaded package, added when it is run.
	upgrade, remove []string
	// root marks commands that need the administrator.
	root bool
	// byHand marks commands shoal cannot run itself: winget cannot
	// replace or delete a program while it is running.
	byHand bool
	// pkg is the release file a system package manager installs from.
	pkg string
}

// command spells a command the way the user would type it.
func (m *manager) command(args []string) string {
	s := strings.Join(args, " ")
	if m.root {
		s = "sudo " + s
	}
	return s
}

// probes are how detectManager looks at the machine: whether a tool is
// installed, and whether a package manager owns a file. Tests replace
// both.
type probes struct {
	has  func(tool string) bool
	owns func(args ...string) bool
}

func (p probes) withDefaults() probes {
	if p.has == nil {
		p.has = func(tool string) bool {
			_, err := exec.LookPath(tool)
			return err == nil
		}
	}
	if p.owns == nil {
		p.owns = func(args ...string) bool {
			if _, err := exec.LookPath(args[0]); err != nil {
				return false
			}
			cmd := exec.Command(args[0], args[1:]...)
			return cmd.Run() == nil
		}
	}
	return p
}

// detectManager names the package manager that installed the program at
// target, or returns nil when none did (install.sh, a zip, a build).
func detectManager(target, goos, goarch string, p probes) *manager {
	p = p.withDefaults()
	slash := filepath.ToSlash(target)
	if i := strings.Index(slash, "/Cellar/shoal/"); i >= 0 {
		brew := filepath.FromSlash(slash[:i] + "/bin/brew")
		if !exists(brew) {
			brew = "brew"
		}
		return &manager{name: "Homebrew",
			upgrade: []string{brew, "upgrade", HomebrewFormula},
			remove:  []string{brew, "uninstall", HomebrewFormula}}
	}
	if goos == "windows" && strings.Contains(strings.ToLower(target), `\winget\packages\`) {
		return &manager{name: "winget", byHand: true,
			upgrade: []string{"winget", "upgrade", "--id", WingetID, "--exact"},
			remove:  []string{"winget", "uninstall", "--id", WingetID, "--exact"}}
	}
	if goos != "linux" || target != systemBinary {
		return nil
	}
	pkg := func(ext string) string { return "shoal-linux-" + goarch + ext }
	switch {
	case p.owns("dpkg-query", "-S", target):
		if p.has("apt-get") {
			return &manager{name: "apt", root: true, pkg: pkg(".deb"),
				upgrade: []string{"apt-get", "install"}, remove: []string{"apt-get", "remove", "shoal"}}
		}
		return &manager{name: "dpkg", root: true, pkg: pkg(".deb"),
			upgrade: []string{"dpkg", "-i"}, remove: []string{"dpkg", "-r", "shoal"}}
	case p.owns("rpm", "-qf", target):
		for _, tool := range []string{"dnf", "zypper", "yum"} {
			if p.has(tool) {
				return &manager{name: tool, root: true, pkg: pkg(".rpm"),
					upgrade: []string{tool, "install"}, remove: []string{tool, "remove", "shoal"}}
			}
		}
		return &manager{name: "rpm", root: true, pkg: pkg(".rpm"),
			upgrade: []string{"rpm", "-U"}, remove: []string{"rpm", "-e", "shoal"}}
	case p.owns("pacman", "-Qo", target):
		return &manager{name: "pacman", root: true, pkg: pkg(".pkg.tar.zst"),
			upgrade: []string{"pacman", "-U"}, remove: []string{"pacman", "-R", "shoal"}}
	}
	return nil
}

// rootInstall is what an upgrade of a system package runs as root: copy
// the checked package somewhere only root can write, check the copy is
// still that package, and hand it to the package manager, whose command
// and arguments follow and to which the copy's path is added last. As
// with rootCopy, nothing the user runs can change the copy between the
// check and the install.
const rootInstall = `set -e
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
pkg="$work/$2"
cp "$1" "$pkg"
if command -v sha256sum >/dev/null 2>&1; then sum=$(sha256sum "$pkg")
else sum=$(shasum -a 256 "$pkg"); fi
if [ "${sum%% *}" != "$3" ]; then
	echo "shoal: the package to install no longer matches the checked download; nothing was changed" >&2
	exit 1
fi
shift 3
"$@" "$pkg"
`

// upgradeWith hands an update to the package manager that installed
// shoal. Homebrew is asked to upgrade; a system package is downloaded from
// the release, checked against its published checksum, and installed by
// the system's package manager through sudo; winget's command is printed
// to run once shoal has exited.
func (o Options) upgradeWith(ctx context.Context, m *manager, latest string) error {
	switch {
	case m.byHand:
		fmt.Fprintf(o.out(), "Shoal was installed with %s, which updates it. It cannot replace shoal while\nshoal is running, so once this command has finished, run:\n  %s\n", m.name, m.command(m.upgrade))
		return nil
	case m.pkg == "":
		fmt.Fprintf(o.out(), "Shoal was installed with %s, so %s updates it:\n  %s\n", m.name, m.name, m.command(m.upgrade))
		return o.runner()(m.upgrade[0], m.upgrade[1:]...)
	}
	fmt.Fprintf(o.out(), "Shoal was installed as a package, so %s installs the new one, %s.\n", m.name, m.pkg)
	sums, err := fetch(ctx, o, o.base()+"/latest/download/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("cannot read the release's checksums: %w", err)
	}
	want, err := checksumFor(sums, m.pkg)
	if err != nil {
		return fmt.Errorf("%w; there may be no package for this machine in %s", err, latest)
	}
	body, err := fetch(ctx, o, o.base()+"/latest/download/"+m.pkg)
	if err != nil {
		return fmt.Errorf("cannot download %s: %w", m.pkg, err)
	}
	if got := sha256.Sum256(body); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("the download of %s does not match its published checksum, so it is damaged or has been altered; nothing was changed", m.pkg)
	}
	fmt.Fprintln(o.out(), "Checksum verified.")
	tmp, err := os.CreateTemp("", "shoal-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	run := o.elevator()
	if run == nil {
		return fmt.Errorf("installing a package needs the administrator, and sudo is not available. Download %s from the release and install it with %s", m.pkg, m.name)
	}
	fmt.Fprintf(o.out(), "sudo may ask for your password; then %s takes over:\n  %s %s\n", m.name, m.command(m.upgrade), m.pkg)
	args := append([]string{"-c", rootInstall, "shoal-upgrade", tmp.Name(), m.pkg, want}, m.upgrade...)
	if err := run("/bin/sh", args...); err != nil {
		return fmt.Errorf("%s did not install %s: %w", m.name, m.pkg, err)
	}
	return nil
}

// runner is how this update runs a command as the user, with the terminal
// attached.
func (o Options) runner() func(string, ...string) error {
	if o.Run != nil {
		return o.Run
	}
	return runAttached
}

// runAttached runs a command with the terminal as its input and output.
func runAttached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
