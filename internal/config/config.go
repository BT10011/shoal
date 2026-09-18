// Package config is the settings shoal keeps between runs, in a small TOML
// file in the per-user configuration directory. Today that is the theme;
// the file is where later settings belong too.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Config is everything the file holds.
type Config struct {
	// Theme is the colour theme last kept in the theme picker, by name.
	Theme string `toml:"theme,omitempty"`
}

const header = "# shoal settings. Written by shoal when a setting changes; safe to edit\n# while shoal is not running. Delete the file to go back to the defaults.\n\n"

// DefaultPath is where the file lives: $XDG_CONFIG_HOME/shoal (or
// ~/.config/shoal) on Linux and the BSDs, ~/Library/Application Support/shoal
// on macOS, %AppData%\shoal on Windows.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no configuration directory: %w", err)
	}
	return filepath.Join(dir, "shoal", "config.toml"), nil
}

// Load reads the file. A file that does not exist yet is not an error: it
// is the defaults.
func Load(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save writes the file whole, through a temporary file renamed into place,
// so a crash part-way never leaves it half-written.
func Save(path string, c Config) error {
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.WriteString(header + string(data)); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Update loads the file, applies change, and saves it, so a change to one
// setting keeps the others.
func Update(path string, change func(*Config)) error {
	c, err := Load(path)
	if err != nil {
		return err
	}
	change(&c)
	return Save(path, c)
}
