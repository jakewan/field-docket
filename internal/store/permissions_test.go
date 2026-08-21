package store

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// closedStoreAt creates a store at path, writes to it, and closes it.
//
// A clean close leaves the database alone on disk: SQLite checkpoints the WAL
// and removes the -wal and -shm sidecars. So this is the shape of a store no
// process is holding, and a spec wanting sidecars needs openStoreAt instead.
func closedStoreAt(t *testing.T, path string) {
	t.Helper()
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store at %s: %v", path, err)
	}
	if _, aerr := st.Append(context.Background(), sample("permissions")); aerr != nil {
		t.Fatalf("append: %v", aerr)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close store: %v", cerr)
	}
}

// openStoreAt creates a store at path and leaves it open, so its -wal and -shm
// sidecars exist for the duration of the spec.
//
// That is the real case the sidecar check answers rather than a contrivance:
// this store takes concurrent writes from several sessions, so another process
// holding it open is the normal state, and a session that died without a clean
// close leaves the sidecars behind too.
func openStoreAt(t *testing.T, path string) {
	t.Helper()
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store at %s: %v", path, err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	if _, aerr := st.Append(context.Background(), sample("permissions")); aerr != nil {
		t.Fatalf("append: %v", aerr)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, serr := os.Lstat(path + suffix); serr != nil {
			t.Fatalf("expected %s sidecar on an open store: %v", suffix, serr)
		}
	}
}

// requirePOSIXModes skips a spec whose subject is a POSIX permission bit.
//
// Windows has no such bits: Go synthesises a mode from the read-only attribute,
// so every writable file reports 0666 and every directory 0777. Asserting
// against that would measure the synthesis rather than anything this package
// does.
func requirePOSIXModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are synthesised on Windows")
	}
}

// chmod sets mode, failing the test rather than returning an error, so a spec's
// setup stays a single line.
func chmod(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// TestUnsafeStoreErrorTellsTheOperatorWhatToDo covers the message an operator
// reads in a tool result with no other context around it.
//
// The half that is easy to lose is the middle one. Being handed a mode and a
// chmod invites reading the whole thing as a question of who may read the file;
// what actually stops the tools is that the record may have been written to, and
// an operator who misses that repairs the permissions and carries on trusting a
// corpus they had reason to doubt.
func TestUnsafeStoreErrorTellsTheOperatorWhatToDo(t *testing.T) {
	path := "/state/field-docket/field-docket.db"
	err := UnsafeStoreError([]PermissionIssue{
		{Path: path, Mode: 0o644},
		{Path: path + "-wal", Mode: 0o640},
	})

	for _, want := range []struct{ fragment, why string }{
		{path, "names the offending database"},
		{path + "-wal", "names the offending sidecar"},
		{"0644", "reports the mode found"},
		{"0640", "reports each mode found, not just the first"},
		{"chmod 0600 " + path + " " + path + "-wal", "repairs every file in one command"},
		{"modified", "says the record itself may have been altered, not merely read"},
		{"copy the database", "offers a way to copy the docket without opening it"},
		{"allow_unsafe_permissions", "names the way to proceed deliberately"},
	} {
		if !strings.Contains(err.Error(), want.fragment) {
			t.Errorf("message %s; it read:\n%s", want.why, err)
		}
	}
}

// TestUnsafeStoreErrorRendersOneRunnableRepair covers the message as a shell
// user meets it: as something to select and paste.
//
// The operator reading this is mid-incident and acting on a single string, so a
// remedy that looks runnable and is not costs them more than no remedy at all —
// they run it, part of it fails, they restart, and the docket refuses again on a
// file they believe they just repaired. One command, one sentence, and no
// punctuation touching a path.
func TestUnsafeStoreErrorRendersOneRunnableRepair(t *testing.T) {
	path := "/state/field-docket/field-docket.db"
	issues := []PermissionIssue{
		{Path: path, Mode: 0o644},
		{Path: path + "-wal", Mode: 0o644},
		{Path: path + "-shm", Mode: 0o640},
	}
	msg := UnsafeStoreError(issues).Error()

	if n := strings.Count(msg, "chmod"); n != 1 {
		t.Errorf("message names chmod %d times, want one command covering every file:\n%s", n, msg)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if !strings.Contains(msg, p) {
			t.Errorf("message omits %s:\n%s", p, msg)
		}
		if strings.Contains(msg, p+".") {
			t.Errorf("a sentence-ending period abuts %s, so a pasted path is wrong:\n%s", p, msg)
		}
	}
}

// TestUnsafeStoreErrorDoesNotSuggestChmodOnASymlink guards a remedy that would
// be wrong rather than merely unhelpful: chmod follows the link, so obeying it
// would change the mode of whatever the link points at — a file the operator may
// not have intended to touch — while leaving the sidecar still redirected.
func TestUnsafeStoreErrorDoesNotSuggestChmodOnASymlink(t *testing.T) {
	path := "/state/field-docket/field-docket.db"
	link := path + "-wal"
	msg := UnsafeStoreError([]PermissionIssue{{Path: path, Mode: 0o644}, {Path: link, Symlink: true}}).Error()

	// Asserted against the repair command rather than the whole message: the
	// prose does mention chmod, to explain why it is the wrong tool here.
	for line := range strings.SplitSeq(msg, "\n") {
		if !strings.Contains(line, "chmod ") {
			continue
		}
		if strings.Contains(line, link) {
			t.Errorf("the repair command chmods a symbolic link, which would change "+
				"whatever it points at; the line read:\n%s", line)
		}
	}
	if !strings.Contains(msg, "symbolic link") {
		t.Errorf("message does not say the sidecar is a symbolic link; it read:\n%s", msg)
	}
}

func TestCheckPermissionsReportsLooseStoreFiles(t *testing.T) {
	requirePOSIXModes(t)

	cases := []struct {
		name string
		// heldOpen selects the fixture: a store another session is holding, so
		// its sidecars exist, versus one sitting closed on disk.
		heldOpen bool
		// loosen mutates the store on disk and returns the paths that should
		// then be reported.
		loosen func(t *testing.T, path string) []string
	}{
		{
			name: "world-readable database",
			loosen: func(t *testing.T, path string) []string {
				chmod(t, path, 0o644)
				return []string{path}
			},
		},
		{
			name: "group-readable database",
			loosen: func(t *testing.T, path string) []string {
				chmod(t, path, 0o640)
				return []string{path}
			},
		},
		{
			name: "world-writable database",
			loosen: func(t *testing.T, path string) []string {
				chmod(t, path, 0o666)
				return []string{path}
			},
		},
		{
			name:     "loose write-ahead log beside a sound database",
			heldOpen: true,
			loosen: func(t *testing.T, path string) []string {
				chmod(t, path+"-wal", 0o644)
				return []string{path + "-wal"}
			},
		},
		{
			name:     "loose shared-memory file beside a sound database",
			heldOpen: true,
			loosen: func(t *testing.T, path string) []string {
				chmod(t, path+"-shm", 0o644)
				return []string{path + "-shm"}
			},
		},
		{
			// This server runs in WAL mode and never writes a rollback journal.
			// The point is that something else did: an out-of-band sqlite3
			// session in the default journal mode leaves one behind, holding
			// page images of the store's contents, and that is exactly the
			// premise the check exists on.
			name: "rollback journal left by an out-of-band session",
			loosen: func(t *testing.T, path string) []string {
				if err := os.WriteFile(path+"-journal", []byte("page images"), 0o644); err != nil {
					t.Fatalf("write journal: %v", err)
				}
				return []string{path + "-journal"}
			},
		},
		{
			name:     "every file loose at once",
			heldOpen: true,
			loosen: func(t *testing.T, path string) []string {
				chmod(t, path, 0o644)
				chmod(t, path+"-wal", 0o644)
				chmod(t, path+"-shm", 0o644)
				return []string{path, path + "-wal", path + "-shm"}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "field-docket.db")
			if tc.heldOpen {
				openStoreAt(t, path)
			} else {
				closedStoreAt(t, path)
			}

			want := tc.loosen(t, path)

			issues, err := CheckPermissions(path)
			if err != nil {
				t.Fatalf("check permissions: %v", err)
			}

			got := make([]string, 0, len(issues))
			for _, issue := range issues {
				got = append(got, issue.Path)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("reported %v, want %v", got, want)
			}
		})
	}
}

// TestCheckPermissionsReportsASymlinkedSidecar covers the case os.Stat cannot
// see: a sidecar replaced by a link reports the *target's* mode, so a link
// pointing at a 0600 file passes while the real -wal contents sit wherever the
// link leads. SQLite treats this as hostile too — it opens -shm with O_NOFOLLOW.
func TestCheckPermissionsReportsASymlinkedSidecar(t *testing.T) {
	requirePOSIXModes(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "field-docket.db")
	closedStoreAt(t, path)

	// The link target is 0600, so following it — which os.Stat would do —
	// clears the check while the -wal contents live wherever the link leads.
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(elsewhere, nil, 0o600); err != nil {
		t.Fatalf("write link target: %v", err)
	}
	if err := os.Symlink(elsewhere, path+"-wal"); err != nil {
		t.Fatalf("symlink sidecar: %v", err)
	}

	issues, err := CheckPermissions(path)
	if err != nil {
		t.Fatalf("check permissions: %v", err)
	}
	if len(issues) != 1 || issues[0].Path != path+"-wal" {
		t.Fatalf("reported %v, want the symlinked -wal", issues)
	}
	if !issues[0].Symlink {
		t.Error("issue does not record that the sidecar is a symlink")
	}
}

// TestCheckPermissionsAcceptsASoundStore is the accept-case, and it is load
// bearing in the opposite direction from the refusal table above: it exists so
// that a later tightening of the bar — ownership, the ancestor chain, the
// directory — cannot start refusing stores that must keep working without
// turning a test red first.
func TestCheckPermissionsAcceptsASoundStore(t *testing.T) {
	requirePOSIXModes(t)

	t.Run("store the server created itself", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "field-docket.db")
		closedStoreAt(t, path)

		issues, err := CheckPermissions(path)
		if err != nil {
			t.Fatalf("check permissions: %v", err)
		}
		if len(issues) != 0 {
			t.Errorf("reported %v on a store it created at 0600", issues)
		}
	})

	t.Run("no store on disk yet", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "field-docket.db")

		issues, err := CheckPermissions(path)
		if err != nil {
			t.Fatalf("check permissions on an absent store: %v", err)
		}
		if len(issues) != 0 {
			t.Errorf("reported %v before the store exists", issues)
		}
	})

	t.Run("loose directory holding a sound database", func(t *testing.T) {
		// The operator may site the store anywhere, including a directory they
		// did not create for it — a 0755 home directory is the ordinary case.
		// The file's own mode is the boundary, so this must pass.
		dir := filepath.Join(t.TempDir(), "sited")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create directory: %v", err)
		}
		path := filepath.Join(dir, "field-docket.db")
		closedStoreAt(t, path)
		chmod(t, dir, 0o755)

		issues, err := CheckPermissions(path)
		if err != nil {
			t.Fatalf("check permissions: %v", err)
		}
		if len(issues) != 0 {
			t.Errorf("reported %v for a directory the operator chose", issues)
		}
	})
}
