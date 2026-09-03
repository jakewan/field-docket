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

	// AllowUnsafePermissions lists store paths, each with a leading ~ expanded,
	// whose files may be reachable beyond their owner without the server
	// declining to serve them.
	//
	// A list rather than a single switch, because the exemption outlives the
	// incident that prompted it: a blanket flag set once would stay in force for
	// every store the binary ever opens, including one this config is pointed at
	// later. Matching a path the operator named is a decision they made about
	// that store.
	AllowUnsafePermissions []string
}

// file mirrors the on-disk YAML shape. It is unexported and separate from Config
// so the public type stays flat without coupling callers to the file's layout.
type file struct {
	Store                  string   `yaml:"store"`
	AllowUnsafePermissions []string `yaml:"allow_unsafe_permissions"`
}

// Load reads the config, resolving its path from override (the --config flag),
// then $FIELD_DOCKET_CONFIG, then the XDG config dir — so one instance can be
// pointed at its own config by a single field-docket-specific knob rather than
// by relocating the general-purpose XDG base.
//
// An absent config yields defaults only where the path was not asked for — the
// XDG default is expected not to exist on most machines. A config named by
// --config or $FIELD_DOCKET_CONFIG must exist and must parse: the caller asked
// for a specific file, so silently ignoring a typo would be worse than refusing
// to start. That applies to a typo in the path as much as one inside the file,
// and the path is the worse of the two — none of the intended configuration
// takes effect, rather than some of it.
func Load(_ context.Context, override string) (*Config, error) {
	path, named, err := configPath(override)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied by design
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !named {
				return &Config{}, nil
			}
			return nil, fmt.Errorf("config %s does not exist; omit --config and $FIELD_DOCKET_CONFIG to start on defaults: %w", path, err)
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

	// Blank entries are dropped for the same reason a blank store path is
	// treated as absent: they carry no intent. Silently keeping one would put an
	// empty string in the allowlist, which no store path can equal but which
	// reads, to someone scanning the config, as though something were exempt.
	var allowed []string
	for _, entry := range f.AllowUnsafePermissions {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		expanded, xerr := expandTilde(trimmed, "allow_unsafe_permissions entry")
		if xerr != nil {
			return nil, xerr
		}
		allowed = append(allowed, expanded)
	}

	return &Config{Store: store, AllowUnsafePermissions: allowed}, nil
}

// configPath resolves the config file path: the trimmed override, then a
// trimmed $FIELD_DOCKET_CONFIG, then $XDG_CONFIG_HOME/field-docket/config.yml
// (falling back to ~/.config when that is unset).
//
// named reports whether the operator asked for this path rather than falling
// through to the XDG default. Load needs the distinction because it decides
// whether an absent file is benign, and only the resolved path is left by then.
//
// Whitespace-only override or env values are ignored so a stray value cannot
// resolve to a nonsense path — and because they are ignored, they are not
// "named" either, so a blank knob still starts on defaults. The XDG variable is
// read directly rather than via os.UserConfigDir so the layout matches the
// documented one on every platform and stays isolable with t.Setenv.
func configPath(override string) (path string, named bool, err error) {
	if p := strings.TrimSpace(override); p != "" {
		expanded, xerr := expandTilde(p, "--config path")
		return expanded, true, xerr
	}
	if p := strings.TrimSpace(os.Getenv("FIELD_DOCKET_CONFIG")); p != "" {
		expanded, xerr := expandTilde(p, "FIELD_DOCKET_CONFIG path")
		return expanded, true, xerr
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", false, fmt.Errorf("resolving config dir: %w", herr)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "field-docket", "config.yml"), false, nil
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
