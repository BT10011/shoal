package selfupdate

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// installerMarker is the comment install.sh writes above the line it adds
// to a shell's startup file. The comment and the line after it are what an
// uninstall takes back out, so a PATH entry the user added themselves is
// left alone.
const installerMarker = "# Added by the Shoal installer"

// Item is one thing an uninstall would remove.
type Item struct {
	Path string
	What string // what it is, in words, for the list shown before removing
	// Line is set for a shell startup file: the PATH entry to take out of
	// it, rather than the file itself.
	Line string
}

// UninstallOptions configure a removal. The caller passes the paths shoal
// actually uses rather than having them worked out again here, so the two
// can never drift apart.
type UninstallOptions struct {
	// Binary is the file to delete; empty means the running binary.
	Binary string
	// ConfigFile and HistoryFile are what shoal writes: the settings file
	// (internal/config) and the history database (internal/store).
	ConfigFile, HistoryFile string
	// Shells overrides the startup files searched for the installer's PATH
	// line; nil means the usual ones for this user.
	Shells []string
	// Out is where the list and the result are written.
	Out io.Writer
	// Confirm is asked before anything is removed. Nil means refuse.
	Confirm func(question string) bool
}

func (o UninstallOptions) out() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return io.Discard
}

// Plan lists what an uninstall would remove, leaving out whatever is not
// there. Nothing is deleted until the list has been shown and agreed to:
// this removes a program and the record of every network it has seen.
func Plan(o UninstallOptions) ([]Item, error) {
	var items []Item
	seen := map[string]bool{}
	add := func(path, what string) {
		if path == "" || seen[path] || !exists(path) {
			return
		}
		seen[path] = true
		items = append(items, Item{Path: path, What: what})
	}

	bin, err := targetPath(o.Binary)
	if err != nil {
		return nil, err
	}
	add(bin, "the shoal program")
	// On macOS both files live in one directory, so these can name the
	// same thing; add skips the repeat.
	add(owned(o.ConfigFile), "settings, including the remembered theme")
	add(owned(o.HistoryFile), "the history of every network shoal has scanned")

	for _, rc := range o.shells() {
		if line, ok := installerLine(rc); ok {
			items = append(items, Item{Path: rc, What: "the PATH entry the installer added", Line: line})
		}
	}
	return items, nil
}

// owned turns a file shoal writes into the thing to delete: the directory
// holding it when shoal owns that directory, so nothing of anyone else's
// is caught, and otherwise the file alone — which is what a history file
// named with --history is.
func owned(file string) string {
	if file == "" {
		return ""
	}
	if dir := filepath.Dir(file); filepath.Base(dir) == "shoal" {
		return dir
	}
	return file
}

// Uninstall removes what Plan found, after Confirm agrees. It carries on
// past whatever it cannot remove and says so at the end, so a binary that
// needs a password does not leave the settings and history behind.
func Uninstall(o UninstallOptions) error {
	items, err := Plan(o)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(o.out(), "Nothing to remove: no shoal binary, settings or history found.")
		return nil
	}
	fmt.Fprintln(o.out(), "This will remove:")
	for _, it := range items {
		if it.Line != "" {
			fmt.Fprintf(o.out(), "  %-44s %s\n", it.Path, it.What)
			fmt.Fprintf(o.out(), "  %-44s   %s\n", "", it.Line)
			continue
		}
		fmt.Fprintf(o.out(), "  %-44s %s\n", it.Path, it.What)
	}
	fmt.Fprintln(o.out())
	if o.Confirm == nil || !o.Confirm("Remove all of this?") {
		fmt.Fprintln(o.out(), "Nothing was removed.")
		return nil
	}

	var failed []Item
	for _, it := range items {
		var err error
		if it.Line != "" {
			err = removeInstallerLine(it.Path)
		} else {
			err = os.RemoveAll(it.Path)
		}
		if err != nil {
			failed = append(failed, it)
			fmt.Fprintf(o.out(), "could not remove %s: %v\n", it.Path, err)
			continue
		}
		fmt.Fprintf(o.out(), "removed %s\n", it.Path)
	}
	if len(failed) > 0 {
		var paths []string
		for _, it := range failed {
			paths = append(paths, it.Path)
		}
		return fmt.Errorf("could not remove %s.\nIf it is under /usr/local, it belongs to the administrator: sudo rm -rf %s",
			strings.Join(paths, ", "), strings.Join(paths, " "))
	}
	fmt.Fprintln(o.out(), "\nShoal is gone. Open a new terminal for the PATH change to take effect.")
	return nil
}

// shells are the startup files install.sh may have written a PATH line to.
// All of them are looked at, not just the current shell's, because the
// shell that installed shoal need not be the one uninstalling it.
func (o UninstallOptions) shells() []string {
	if o.Shells != nil {
		return o.Shells
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".config", "fish", "config.fish"),
	}
}

// installerLine reports the PATH line the installer added to a startup
// file, if it is still there.
func installerLine(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	marked := false
	for s.Scan() {
		line := s.Text()
		if marked && strings.TrimSpace(line) != "" {
			return line, true
		}
		if strings.TrimSpace(line) == installerMarker {
			marked = true
		}
	}
	return "", false
}

// removeInstallerLine takes the installer's comment and the line under it
// back out of a startup file, and nothing else: a PATH entry the user wrote
// themselves carries no marker and is left where it is.
func removeInstallerLine(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != installerMarker {
			kept = append(kept, lines[i])
			continue
		}
		// Drop the marker, the line it introduces, and the blank line the
		// installer put before it, so repeated installs leave no gaps.
		if n := len(kept); n > 0 && strings.TrimSpace(kept[n-1]) == "" {
			kept = kept[:n-1]
		}
		for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
			i++
		}
		i++ // the PATH line itself
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")), mode)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
