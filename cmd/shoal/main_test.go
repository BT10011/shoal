package main

import (
	"flag"
	"testing"

	"github.com/BT10011/shoal/internal/config"
)

func parse(t *testing.T, args ...string) (*flag.FlagSet, string) {
	t.Helper()
	fs := flag.NewFlagSet("shoal", flag.ContinueOnError)
	theme := fs.String("theme", "catppuccin-mocha", "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs, *theme
}

func TestThemeSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	fs, flagTheme := parse(t)
	name, save := themeSettings(fs, flagTheme)
	if name != "catppuccin-mocha" || save == nil {
		t.Fatalf("nothing saved yet: %q, save=%v", name, save != nil)
	}
	if err := save("nord"); err != nil {
		t.Fatal(err)
	}

	fs, flagTheme = parse(t)
	if name, _ := themeSettings(fs, flagTheme); name != "nord" {
		t.Fatalf("the kept theme should be used next time: %q", name)
	}

	fs, flagTheme = parse(t, "--theme", "vt100")
	if name, _ := themeSettings(fs, flagTheme); name != "vt100" {
		t.Fatalf("--theme wins for this run: %q", name)
	}
	path, _ := config.DefaultPath()
	if c, _ := config.Load(path); c.Theme != "nord" {
		t.Fatalf("--theme must not replace the remembered theme: %q", c.Theme)
	}
}
