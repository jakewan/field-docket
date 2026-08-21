package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSidecarsInheritDatabaseMode pins the claim the store's whole
// create-it-first dance rests on: SQLite stamps -wal and -shm with the main
// database's mode when it creates them, during the first WAL-mode open. That is
// why Open pre-creates the database at 0600 with O_EXCL rather than letting the
// driver create it and correcting afterwards — a chmod would land too late.
//
// Asserting equality rather than "no group or other bits" is deliberate. The
// latter passes on a 0600 database beside a 0400 -wal, which would mean
// inheritance had stopped happening while the spec stayed green; the modes have
// to actually match for the reasoning in Open to hold.
func TestSidecarsInheritDatabaseMode(t *testing.T) {
	requirePOSIXModes(t)

	cases := []struct {
		name string
		// mode is applied to the database before the sidecars are created, so
		// a mode other than the store's own proves this is inheritance rather
		// than a constant the driver happens to share with Open.
		mode os.FileMode
	}{
		{name: "the mode the store creates", mode: 0o600},
		{name: "a mode the store would never choose", mode: 0o640},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "field-docket.db")
			closedStoreAt(t, path)
			chmod(t, path, tc.mode)

			// Reopening is what creates the sidecars; they do not survive the
			// clean close above.
			openStoreAt(t, path)

			want, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("stat database: %v", err)
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				info, serr := os.Lstat(path + suffix)
				if serr != nil {
					t.Fatalf("stat %s sidecar: %v", suffix, serr)
				}
				if got := info.Mode().Perm(); got != want.Mode().Perm() {
					t.Errorf("%s mode = %04o, want %04o inherited from the database",
						suffix, got, want.Mode().Perm())
				}
			}
		})
	}
}
