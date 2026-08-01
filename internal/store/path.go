package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultPath returns the store location under the XDG state directory.
//
// XDG_STATE_HOME is read with os.Getenv rather than through os.UserConfigDir so
// the resolution is identical on every platform and can be exercised with
// t.Setenv. The fallback is not a rare branch: agent shells are non-interactive
// and do not source the profile that exports the XDG variables, so
// ~/.local/state is the live path in the environment this server actually runs
// in.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory for the default store path: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "field-docket", "field-docket.db"), nil
}
