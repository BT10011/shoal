package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingFileIsTheDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || c.Theme != "" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}

func TestSaveThenLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shoal", "config.toml")
	if err := Save(path, Config{Theme: "nord"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.Theme != "nord" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "# shoal settings.") || !strings.Contains(string(data), "theme = 'nord'") {
		t.Errorf("file should explain itself and hold the theme:\n%s", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("no temporary files should be left behind: %v", entries)
	}
}

func TestUpdateChangesOneSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Update(path, func(c *Config) { c.Theme = "dracula" }); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(c *Config) { c.Theme = "vt100" }); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(path); c.Theme != "vt100" {
		t.Fatalf("theme = %q", c.Theme)
	}
}

func TestABrokenFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("theme = [not toml"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("a broken file should be named in the error: %v", err)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, filepath.Join("shoal", "config.toml")) {
		t.Fatalf("path = %q", p)
	}
}
