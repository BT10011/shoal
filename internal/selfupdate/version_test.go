package selfupdate

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestAhead(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"v2.0.0-rc.1", "v1.0.0", true},
		{"v2.0.0", "v2.0.0-rc.1", true},
		{"v2.0.0-rc.1", "v2.0.0", false},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v2.0.0", false},
		{"v10.0.0", "v9.0.0", true},
		{"v1.10.0", "v1.9.0", true},
		{"v2.0.0-rc.10", "v2.0.0-rc.2", true},
		{"v2.0.0-rc.2", "v2.0.0-rc.10", false},
		{"v2.0.0-beta.1", "v2.0.0-alpha.3", true},
		{"v2.0.0-alpha", "v2.0.0-1", true},
		{"v2.0.0-rc.1.1", "v2.0.0-rc.1", true},
		{"v2.0.0+build.5", "v2.0.0", false},
		// A build from source between tags is a pre-release of the tag
		// git describe names, so it is not ahead of that tag.
		{"v1.0.0-45-gf816a12-dirty", "v1.0.0", false},
		{"dev", "v1.0.0", false},
		{"v2.0", "v1.0.0", false},
		{"v2.0.0", "latest", false},
		{"v2.0.0-", "v1.0.0", false},
		{"2.0.0", "v1.0.0", false},
	} {
		if got := Ahead(tc.current, tc.latest); got != tc.want {
			t.Errorf("Ahead(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestUpdateNeverGoesBackToAnOlderRelease(t *testing.T) {
	// A release candidate is newer than the latest release, which skips
	// pre-releases: updating it to that release would undo it.
	srv := release(t, "v1.0.0", nil)
	defer srv.Close()
	target := installed(t, "candidate")
	var out bytes.Buffer
	if err := Update(context.Background(), Options{
		BaseURL: srv.URL + "/releases", Version: "v2.0.0-rc.1", Target: target,
		GOOS: "linux", GOARCH: "amd64", Out: &out,
		Elevate: refuse, Run: refuse,
	}); err != nil {
		t.Fatalf("Update = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "newer than the latest release (v1.0.0)") {
		t.Errorf("output = %q, want it to say this build is newer", out.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "candidate" {
		t.Errorf("binary was replaced with an older release")
	}
}
