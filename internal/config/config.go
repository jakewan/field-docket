// Package config loads the field-docket server configuration.
//
// Every field is optional and the whole file is optional: with no config at all
// the server starts on its defaults. That is the deliberate difference from a
// config loader whose file carries a required key — there is nothing here a
// caller must supply, so treating an absent file as an error would make the
// server unusable out of the box for no gain.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// Config is the validated server configuration.
type Config struct {
	// Store is the path to the SQLite store, with a leading ~ expanded, or ""
	// when the config omits it. An empty Store means "use the default
	// location"; resolving that default belongs to the composition root, so
	// config stays ignorant of the store package.
	Store string
}

// file mirrors the on-disk YAML shape. It is unexported and separate from Config
// so the public type stays flat without coupling callers to the file's layout.
type file struct {
	Store string `yaml:"store"`
}

// Load reads the config, resolving its path from override (the --config flag),
// then $FIELD_DOCKET_CONFIG, then the XDG config dir — so one instance can be
// pointed at its own config by a single field-docket-specific knob rather than
// by relocating the general-purpose XDG base.
//
// A missing file yields defaults, not an error. A file that exists but cannot be
// read or parsed is an error: the caller asked for a specific config, so
// silently ignoring a typo in it would be worse than refusing to start.
func Load(_ context.Context, override string) (*Config, error) {
	path, err := configPath(override)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied by design
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var f file
	if uerr := yaml.Unmarshal(data, &f); uerr != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, uerr)
	}

	// A blank store path is treated as absent: the field is optional, and an
	// empty or whitespace-only value carries no intent, so it resolves to "" and
	// the caller applies the default. The trimmed value is what gets expanded,
	// so surrounding whitespace can neither defeat ~ expansion nor leak into the
	// resolved path.
	var store string
	if trimmed := strings.TrimSpace(f.Store); trimmed != "" {
		store, err = expandTilde(trimmed, "store path")
		if err != nil {
			return nil, err
		}
	}

	return &Config{Store: store}, nil
}

// configPath resolves the config file path: the trimmed override, then a
// trimmed $FIELD_DOCKET_CONFIG, then $XDG_CONFIG_HOME/field-docket/config.yml
// (falling back to ~/.config when that is unset).
//
// Whitespace-only override or env values are ignored so a stray value cannot
// resolve to a nonsense path. The XDG variable is read directly rather than via
// os.UserConfigDir so the layout matches the documented one on every platform
// and stays isolable with t.Setenv.
func configPath(override string) (string, error) {
	if p := strings.TrimSpace(override); p != "" {
		return expandTilde(p, "--config path")
	}
	if p := strings.TrimSpace(os.Getenv("FIELD_DOCKET_CONFIG")); p != "" {
		return expandTilde(p, "FIELD_DOCKET_CONFIG path")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "field-docket", "config.yml"), nil
}

// expandTilde rewrites a leading ~ or ~/ to the user's home directory. Paths
// without a leading ~ are returned unchanged. field names what is being expanded
// so a failure points at the offending input.
func expandTilde(path, field string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expanding ~ in %s: %w", field, err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
