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
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
// for by the same name; the folder inside says which build it is.
func Asset(goos, goarch string) string {
	return fmt.Sprintf("shoal-%s-%s.tar.gz", goos, goarch)
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

	binary, err := binaryFromArchive(archive)
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

// binaryFromArchive pulls the shoal binary out of a release archive, which
// holds it in a folder named for the build.
func binaryFromArchive(archive []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("the download is not a gzip archive: %w", err)
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
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "shoal" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxDownload))
		if err != nil {
			return nil, fmt.Errorf("cannot read shoal out of the archive: %w", err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("the release archive holds no shoal binary")
}

// replace writes the new binary beside the old one and renames it over the
// top. A running binary cannot be written to (the kernel refuses with
// ETXTBSY), but it can be renamed over, and a rename within one directory
// is atomic: either the old shoal is there or the new one is, never half of
// either.
func replace(target string, binary []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".shoal-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w\n\nTo update a shoal installed there, run: sudo shoal --update", dir, err)
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
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("cannot replace %s: %w\n\nTo update a shoal installed there, run: sudo shoal --update", target, err)
	}
	return nil
}
