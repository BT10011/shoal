package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release serves what a GitHub release serves: a /latest that redirects to
// the tag, and files under /latest/download.
func release(t *testing.T, tag string, files map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/tag/"+tag, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Release %s", tag)
	})
	mux.HandleFunc("/releases/latest/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/releases/latest/download/")
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	return httptest.NewServer(mux)
}

// archiveOf packs a binary the way make release does: in a folder named
// for the build.
func archiveOf(t *testing.T, tag, goos, goarch string, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	folder := fmt.Sprintf("shoal-%s-%s-%s", tag, goos, goarch)
	for _, f := range []struct {
		name string
		body []byte
	}{
		{folder + "/README.md", []byte("readme")},
		{folder + "/shoal", binary},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sums(entries map[string][]byte) []byte {
	var b strings.Builder
	for name, body := range entries {
		sum := sha256.Sum256(body)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return []byte(b.String())
}

// installed writes a stand-in for a shoal binary already on disk.
func installed(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shoal")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUpdateReplacesTheBinaryWithTheLatestRelease(t *testing.T) {
	asset := Asset("linux", "amd64")
	archive := archiveOf(t, "v1.2.0", "linux", "amd64", []byte("the new shoal"))
	srv := release(t, "v1.2.0", map[string][]byte{
		asset:        archive,
		"SHA256SUMS": sums(map[string][]byte{asset: archive}),
	})
	defer srv.Close()

	target := installed(t, "the old shoal")
	var out bytes.Buffer
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0", Target: target,
		GOOS: "linux", GOARCH: "amd64", Out: &out,
	})
	if err != nil {
		t.Fatalf("Update = %v, want nil", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the new shoal" {
		t.Errorf("binary = %q, want the downloaded one", got)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v (err %v), want the old binary's 0755", fi.Mode().Perm(), err)
	}
	for _, want := range []string{"v1.2.0", "Checksum verified"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestUpdateRefusesADownloadThatDoesNotMatchItsChecksum(t *testing.T) {
	// The case that matters: a tampered or damaged archive must leave the
	// installed binary exactly as it was.
	asset := Asset("linux", "amd64")
	archive := archiveOf(t, "v1.2.0", "linux", "amd64", []byte("tampered"))
	srv := release(t, "v1.2.0", map[string][]byte{
		asset:        archive,
		"SHA256SUMS": sums(map[string][]byte{asset: []byte("something else entirely")}),
	})
	defer srv.Close()

	target := installed(t, "the old shoal")
	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0", Target: target,
		GOOS: "linux", GOARCH: "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Update = %v, want a refusal naming the checksum", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "the old shoal" {
		t.Errorf("binary = %q, want the old one left untouched", got)
	}
}

func TestUpdateDoesNothingWhenAlreadyLatest(t *testing.T) {
	srv := release(t, "v1.2.0", nil)
	defer srv.Close()
	target := installed(t, "current")
	var out bytes.Buffer
	if err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.2.0", Target: target,
		GOOS: "linux", GOARCH: "amd64", Out: &out,
	}); err != nil {
		t.Fatalf("Update = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "Already the latest") {
		t.Errorf("output = %q, want it to say so", out.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "current" {
		t.Errorf("binary was rewritten for no reason")
	}
}

func TestUpdateAsksBeforeReplacingABuildFromSource(t *testing.T) {
	asset := Asset("linux", "amd64")
	archive := archiveOf(t, "v1.2.0", "linux", "amd64", []byte("released"))
	srv := release(t, "v1.2.0", map[string][]byte{
		asset: archive, "SHA256SUMS": sums(map[string][]byte{asset: archive}),
	})
	defer srv.Close()

	for _, tc := range []struct {
		name    string
		version string
		confirm func(string) bool
		want    string
	}{
		{"a dirty tree, refused", "v1.1.0-dirty", func(string) bool { return false }, "the old shoal"},
		{"a build from source, refused", "dev", func(string) bool { return false }, "the old shoal"},
		{"a build from source, agreed", "dev", func(string) bool { return true }, "released"},
		{"between tags, agreed", "v1.1.0-3-gabc1234", func(string) bool { return true }, "released"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := installed(t, "the old shoal")
			err := Update(context.Background(), Options{
				BaseURL: srv.URL + "/releases", Version: tc.version, Target: target,
				GOOS: "linux", GOARCH: "amd64", Confirm: tc.confirm,
			})
			if got, _ := os.ReadFile(target); string(got) != tc.want {
				t.Errorf("binary = %q, want %q (err %v)", got, tc.want, err)
			}
		})
	}
}

func TestUpdateSaysWhenThereIsNoBuildForThisMachine(t *testing.T) {
	asset := Asset("linux", "amd64")
	archive := archiveOf(t, "v1.2.0", "linux", "amd64", []byte("released"))
	srv := release(t, "v1.2.0", map[string][]byte{
		asset: archive, "SHA256SUMS": sums(map[string][]byte{asset: archive}),
	})
	defer srv.Close()

	err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v1.1.0", Target: installed(t, "old"),
		GOOS: "windows", GOARCH: "arm64",
	})
	if err == nil || !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("Update = %v, want it to name the platform with no build", err)
	}
}

func TestLatestVersionReadsTheTagFromTheRedirect(t *testing.T) {
	srv := release(t, "v9.9.9", nil)
	defer srv.Close()
	got, err := LatestVersion(context.Background(), Options{BaseURL: srv.URL + "/releases"})
	if err != nil || got != "v9.9.9" {
		t.Fatalf("LatestVersion = %q, %v; want v9.9.9", got, err)
	}
}

func TestLatestVersionExplainsAPrivateOrUnreleasedRepository(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := LatestVersion(context.Background(), Options{BaseURL: srv.URL + "/releases"})
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("LatestVersion error = %v, want it to explain the likely cause", err)
	}
}

func TestLooksReleased(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"v1.0.0", true},
		{"v1.0.0-beta.1", true},
		{"dev", false},
		{"", false},
		{"v1.0.0-dirty", false},
		{"v1.0.0-3-gabc1234", false},
	} {
		if got := looksReleased(tc.version); got != tc.want {
			t.Errorf("looksReleased(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}
