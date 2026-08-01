package store

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathFollowsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/custom/state")

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	want := filepath.Join("/custom/state", "field-docket", "field-docket.db")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// TestDefaultPathFallsBackToLocalState covers the live path, not an edge case.
//
// Agent shells are non-interactive and do not source the profile that exports
// the XDG variables, so the fallback is what actually resolves in the
// environment this server runs in.
func TestDefaultPathFallsBackToLocalState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/example")

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	want := filepath.Join("/home/example", ".local", "state", "field-docket", "field-docket.db")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
