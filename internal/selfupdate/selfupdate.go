// Package selfupdate replaces the running shoal binary with the latest
// published release, and removes shoal and everything it has written.
//
// It does the same work as install.sh, in Go: ask which release is latest,
// download the build for this machine, check it against the release's
// published checksums, and put it where the running binary is. Doing it
// here rather than by fetching and running the installer keeps shoal from
// piping a script off the internet into a shell, and means an update needs
// nothing installed but shoal itself.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository releases are fetched from. A build
// aimed at somewhere else — a beta handed out from a public download-only
// repository — has it stamped in at build time, the way install.sh has it
// rewritten. See the release target in the Makefile.
const DefaultRepo = "BT10011/shoal"

// maxDownload bounds what will be read from the network. A release archive
// is a few megabytes; anything approaching this is not one.
const maxDownload = 128 << 20

// Options configure an update.
type Options struct {
	// Repo is the GitHub owner/name releases come from.
	Repo string
	// BaseURL overrides where releases are fetched from, for a mirror or a
	// test. Default: https://github.com/<Repo>/releases
	BaseURL string
	// Version is the running build, as `shoal version` prints it.
	Version string
	// Target is the file to replace; empty means the running binary.
	Target string
	// Client is the HTTP client to use; nil means a 5-minute one.
	Client *http.Client
	// Out is where progress is written.
	Out io.Writer
	// Confirm is asked before replacing a binary that was not installed
	// from a release. Nil means refuse rather than guess.
	Confirm func(question string) bool
	// GOOS and GOARCH name the build to fetch; empty means this machine's.
	GOOS, GOARCH string
}

func (o Options) repo() string {
	if o.Repo != "" {
		return o.Repo
	}
	return DefaultRepo
}

func (o Options) base() string {
	if o.BaseURL != "" {
		return strings.TrimSuffix(o.BaseURL, "/")
	}
	return "https://github.com/" + o.repo() + "/releases"
}

func (o Options) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (o Options) out() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return io.Discard
}

func (o Options) platform() (string, string) {
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

// Asset is the release file holding the build for a platform. The name
// carries no version, so "the latest release's" build can always be asked
// for by the same name; the folder inside says which build it is. Windows
// builds come as a .zip, which Windows opens without anything installed.
func Asset(goos, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("shoal-%s-%s.zip", goos, goarch)
	}
	return fmt.Sprintf("shoal-%s-%s.tar.gz", goos, goarch)
}

// binaryName is the program's file name inside a platform's archive.
func binaryName(goos string) string {
	if goos == "windows" {
		return "shoal.exe"
	}
	return "shoal"
}

// asAdministrator says how to run a shoal command with the rights to
// change a folder only the administrator can write to.
func asAdministrator(command string) string {
	if runtime.GOOS == "windows" {
		return "open a terminal with Run as administrator and run: " + command
	}
	return "run: sudo " + command
}

// Update fetches the latest release and replaces the running binary with
// it. It reports what it did, in full, because replacing the program the
// user is running is not something to do quietly.
func Update(ctx context.Context, o Options) error {
	target, err := targetPath(o.Target)
	if err != nil {
		return err
	}
	goos, goarch := o.platform()
	fmt.Fprintf(o.out(), "Shoal %s, installed at %s\n", o.Version, target)

	latest, err := LatestVersion(ctx, o)
	if err != nil {
		return err
	}
	switch {
	case latest == o.Version:
		fmt.Fprintf(o.out(), "Already the latest release (%s). Nothing to do.\n", latest)
		return nil
	case !looksReleased(o.Version):
		// A build from source, or one with uncommitted changes. Replacing
		// it with a published release throws that build away.
		q := fmt.Sprintf("This build (%s) did not come from a release. Replace it with %s?", o.Version, latest)
		if o.Confirm == nil || !o.Confirm(q) {
			return fmt.Errorf("not updating %s, which was not installed from a release", o.Version)
		}
	}
	fmt.Fprintf(o.out(), "Updating to %s (%s/%s)...\n", latest, goos, goarch)

	asset := Asset(goos, goarch)
	sums, err := fetch(ctx, o, o.base()+"/latest/download/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("cannot read the release's checksums: %w", err)
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return fmt.Errorf("%w; there may be no %s/%s build in %s", err, goos, goarch, latest)
	}
	archive, err := fetch(ctx, o, o.base()+"/latest/download/"+asset)
	if err != nil {
		return fmt.Errorf("cannot download %s: %w", asset, err)
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("the download of %s does not match its published checksum, so it is damaged or has been altered; nothing was changed", asset)
	}
	fmt.Fprintln(o.out(), "Checksum verified.")

	binary, err := binaryFromArchive(archive, binaryName(goos))
	if err != nil {
		return err
	}
	if err := replace(target, binary); err != nil {
		return err
	}
	fmt.Fprintf(o.out(), "Replaced %s with %s.\n", target, latest)
	reportRawAccess(o.out(), target, goos)
	return nil
}

// LatestVersion asks which release is latest, without the GitHub API: the
// releases/latest page redirects to the tag, so the tag is in the URL that
// answers. That needs no token and is not rate-limited the way the API is.
func LatestVersion(ctx context.Context, o Options) (string, error) {
	url := o.base() + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s said %s; if this repository is private or has no release yet, there is nothing to update to", url, resp.Status)
	}
	final := resp.Request.URL.Path
	tag := final[strings.LastIndex(final, "/")+1:]
	if tag == "" || tag == "latest" {
		return "", fmt.Errorf("%s did not say which release is latest", url)
	}
	return tag, nil
}

// looksReleased reports whether a version string is a plain release tag
// rather than a build from source. make stamps in `git describe`, so a
// build between tags carries a commit and a dirty tree carries "-dirty".
func looksReleased(v string) bool {
	if v == "" || v == "dev" || strings.HasSuffix(v, "-dirty") {
		return false
	}
	return strings.HasPrefix(v, "v") && !strings.Contains(v, "-g")
}

// targetPath is the file an update replaces: the running binary, with any
// symlinks resolved so the real file is written rather than the link.
func targetPath(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running shoal binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func fetch(ctx context.Context, o Options, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s said %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// checksumFor finds an asset's line in a SHA256SUMS file.
func checksumFor(sums []byte, asset string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not listed in the release's SHA256SUMS", asset)
}

// binaryFromArchive pulls the program, the file called name, out of a
// release archive, which holds it in a folder named for the build. The
// archive is a .tar.gz, or a .zip for Windows.
func binaryFromArchive(archive []byte, name string) ([]byte, error) {
	if bytes.HasPrefix(archive, []byte("PK\x03\x04")) {
		return binaryFromZip(archive, name)
	}
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("the download is not a gzip or zip archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read the release archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != name {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxDownload))
		if err != nil {
			return nil, fmt.Errorf("cannot read %s out of the archive: %w", name, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("the release archive holds no %s", name)
}

// binaryFromZip is binaryFromArchive for a .zip. Names in a zip use
// forward slashes whatever made it, so path, not filepath, splits them.
func binaryFromZip(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("cannot read the release archive: %w", err)
	}
	for _, f := range zr.File {
		if !f.Mode().IsRegular() || path.Base(f.Name) != name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("cannot read %s out of the archive: %w", name, err)
		}
		defer r.Close()
		b, err := io.ReadAll(io.LimitReader(r, maxDownload))
		if err != nil {
			return nil, fmt.Errorf("cannot read %s out of the archive: %w", name, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("the release archive holds no %s", name)
}

// replace writes the new binary beside the old one and renames it over the
// top. A running binary cannot be written to (the kernel refuses with
// ETXTBSY), but it can be renamed over, and a rename within one directory
// is atomic: either the old shoal is there or the new one is, never half of
// either. Windows refuses even the rename over a running program, so there
// the old one is moved aside first (swapAside).
func replace(target string, binary []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".shoal-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w\n\nTo update a shoal installed there, %s", dir, err, asAdministrator("shoal --update"))
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename has happened
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	put := os.Rename
	if runtime.GOOS == "windows" {
		put = swapAside
	}
	if err := put(tmp.Name(), target); err != nil {
		return fmt.Errorf("cannot replace %s: %w\n\nTo update a shoal installed there, %s", target, err, asAdministrator("shoal --update"))
	}
	return nil
}

// oldSuffix marks the copy an update on Windows moves aside.
const oldSuffix = ".old"

// swapAside puts next where target is without writing to or deleting
// target, which Windows refuses while that program is running: it renames
// target to target.old, which Windows allows, then next into its place,
// undoing the first rename if the second fails. The running shoal carries
// on from target.old, which the next start deletes (RemoveLeftovers).
func swapAside(next, target string) error {
	removeLeftovers(target)
	old := target + oldSuffix
	// A leftover still held open: an older shoal is running from it.
	for n := 2; exists(old); n++ {
		old = target + oldSuffix + "-" + strconv.Itoa(n)
	}
	if err := os.Rename(target, old); err != nil {
		return fmt.Errorf("cannot move the running program aside: %w", err)
	}
	if err := os.Rename(next, target); err != nil {
		if back := os.Rename(old, target); back != nil {
			return fmt.Errorf("%w; the old program could not be put back either (%v), and is at %s", err, back, old)
		}
		return err
	}
	return nil
}

// leftovers lists the copies earlier updates moved aside from target:
// target.old, and target.old-2 and on. Only those names, so a file of the
// user's such as shoal.exe.old-backup is never taken for one.
func leftovers(target string) []string {
	var found []string
	if exists(target + oldSuffix) {
		found = append(found, target+oldSuffix)
	}
	more, _ := filepath.Glob(target + oldSuffix + "-*")
	for _, p := range more {
		if n, err := strconv.Atoi(strings.TrimPrefix(p, target+oldSuffix+"-")); err == nil && n >= 2 {
			found = append(found, p)
		}
	}
	return found
}

// removeLeftovers deletes what it can of them; one still running stays.
func removeLeftovers(target string) {
	for _, p := range leftovers(target) {
		os.Remove(p)
	}
}

// RemoveLeftovers deletes the copy of the program an earlier update moved
// aside on Windows, now that it has stopped running. It is called as shoal
// starts, says nothing, and does nothing on other platforms, where an
// update leaves nothing behind and a file named like one is the user's.
func RemoveLeftovers() {
	if runtime.GOOS != "windows" {
		return
	}
	if target, err := targetPath(""); err == nil {
		removeLeftovers(target)
	}
}
