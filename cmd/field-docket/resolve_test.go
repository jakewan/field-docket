package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jakewan/field-docket/internal/store"
)

// seedStore creates a real store at path and closes it, so the permission check
// has something to inspect.
func seedStore(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close seeded store: %v", cerr)
	}
}

// writeConfigFor points a config at storePath and returns the --config path.
func writeConfigFor(t *testing.T, storePath, extra string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	body := "store: " + storePath + "\n" + extra
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestResolveStoreGatesOnPermissions is the seam spec: it is the only place that
// proves the config key reaches the check.
//
// Without it the allowlist could be parsed correctly, the check could work
// correctly, every package test could pass, and the shipped binary could still
// carry a config key that does nothing — because nothing joined the two. That
// failure would be invisible until an operator needed the exemption, which is
// exactly when they cannot afford it not to work.
func TestResolveStoreGatesOnPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are synthesised on Windows")
	}

	tests := []struct {
		name string
		// mode is applied to the database before resolving.
		mode os.FileMode
		// allowlisted puts the store's own path in allow_unsafe_permissions.
		allowlisted  bool
		wantUnusable bool
	}{
		{name: "sound store", mode: 0o600, wantUnusable: false},
		{name: "loose store", mode: 0o644, wantUnusable: true},
		{name: "loose store the operator allowed", mode: 0o644, allowlisted: true, wantUnusable: false},
		{name: "sound store on the allowlist", mode: 0o600, allowlisted: true, wantUnusable: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "field-docket.db")
			seedStore(t, storePath)
			if err := os.Chmod(storePath, tt.mode); err != nil {
				t.Fatalf("chmod store: %v", err)
			}

			extra := ""
			if tt.allowlisted {
				extra = "allow_unsafe_permissions:\n  - " + storePath + "\n"
			}
			configPath := writeConfigFor(t, storePath, extra)

			target, err := resolveStore(context.Background(), configPath)
			if err != nil {
				t.Fatalf("resolve store: %v", err)
			}
			if target.path != storePath {
				t.Errorf("path = %q, want %q", target.path, storePath)
			}
			if got := target.unusable != nil; got != tt.wantUnusable {
				t.Errorf("unusable = %v (%v), want %v", got, target.unusable, tt.wantUnusable)
			}
			if tt.wantUnusable && !strings.Contains(target.unusable.Error(), storePath) {
				t.Errorf("reason does not name the store: %v", target.unusable)
			}
		})
	}
}

// TestResolveStoreMatchesTheAllowlistByPathNotSpelling covers the way this
// escape hatch is most likely to be reached for: an operator reads the refusal,
// which prints the store path as their config spelled it, and writes the path
// into allow_unsafe_permissions in whatever form comes to hand — commonly the
// absolute one, even where the config names the store relatively.
//
// Comparing the two spellings literally would leave that operator with a config
// that looks correct, a docket that keeps refusing, and nothing said about why.
// A silent non-match is the worst failure available to the one mechanism that
// exists for when the guard is wrong.
func TestResolveStoreMatchesTheAllowlistByPathNotSpelling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are synthesised on Windows")
	}

	t.Run("entry spelled with a detour through the parent", func(t *testing.T) {
		dir := t.TempDir()
		storePath := filepath.Join(dir, "field-docket.db")
		seedStore(t, storePath)
		if err := os.Chmod(storePath, 0o644); err != nil {
			t.Fatalf("chmod store: %v", err)
		}

		// Concatenated rather than joined: filepath.Join cleans its result, which
		// would collapse the detour before the config ever saw it and leave this
		// case asserting nothing.
		detour := dir + "/../" + filepath.Base(dir) + "/field-docket.db"
		configPath := writeConfigFor(t, storePath, "allow_unsafe_permissions:\n  - "+detour+"\n")

		target, err := resolveStore(context.Background(), configPath)
		if err != nil {
			t.Fatalf("resolve store: %v", err)
		}
		if target.unusable != nil {
			t.Errorf("an entry naming the same file did not exempt it: %v", target.unusable)
		}
	})

	t.Run("absolute entry for a relatively named store", func(t *testing.T) {
		dir := t.TempDir()
		storePath := filepath.Join(dir, "field-docket.db")
		seedStore(t, storePath)
		if err := os.Chmod(storePath, 0o644); err != nil {
			t.Fatalf("chmod store: %v", err)
		}

		// The config names the store relatively, so everything downstream sees
		// the relative spelling — including the path printed in the refusal.
		t.Chdir(dir)
		configPath := writeConfigFor(t, "field-docket.db",
			"allow_unsafe_permissions:\n  - "+storePath+"\n")

		target, err := resolveStore(context.Background(), configPath)
		if err != nil {
			t.Fatalf("resolve store: %v", err)
		}
		if target.unusable != nil {
			t.Errorf("an absolute entry did not exempt the same file named relatively: %v", target.unusable)
		}
	})
}

// TestResolveStoreExemptsOnlyTheListedStore pins the grain of the allowlist. An
// entry naming one store must not clear a different one — that is the whole
// reason the key is a list of paths rather than a single switch, and a matcher
// that ignored the path would pass every other spec here.
func TestResolveStoreExemptsOnlyTheListedStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are synthesised on Windows")
	}

	dir := t.TempDir()
	listed := filepath.Join(dir, "listed.db")
	unlisted := filepath.Join(dir, "unlisted.db")
	seedStore(t, listed)
	seedStore(t, unlisted)
	for _, p := range []string{listed, unlisted} {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}

	configPath := writeConfigFor(t, unlisted, "allow_unsafe_permissions:\n  - "+listed+"\n")

	target, err := resolveStore(context.Background(), configPath)
	if err != nil {
		t.Fatalf("resolve store: %v", err)
	}
	if target.unusable == nil {
		t.Error("an entry for a different store exempted this one")
	}
}
