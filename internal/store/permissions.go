package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
)

// PermissionIssue names one store file reachable by someone other than its
// owner. Symlink distinguishes the two ways that happens, because the remedy
// differs: a loose mode is repaired with chmod, a replaced sidecar is not.
type PermissionIssue struct {
	Path    string
	Mode    fs.FileMode
	Symlink bool
}

// String renders an issue for an operator-facing message.
func (p PermissionIssue) String() string {
	if p.Symlink {
		return fmt.Sprintf("%s is a symbolic link", p.Path)
	}
	return fmt.Sprintf("%s has mode %04o", p.Path, p.Mode.Perm())
}

// CheckPermissions reports the files of the store at path that are reachable
// beyond their owner: the database itself and, when they exist, its -wal and
// -shm sidecars.
//
// A store the server created is 0600 and reports nothing, as does a path with
// no store on it yet. Findings therefore mean something outside this program
// touched the files — which is why the caller treats them as a question about
// the record's provenance and not only about who can read it.
//
// The store's *directory* is deliberately not inspected. The operator may site
// the store anywhere, including a directory they did not create for it, and a
// 0600 file inside a 0755 directory is not readable by others. Refusing on the
// directory would fire on an ordinary `store: ~/field-docket.db` — whose
// directory is the home directory — and the repair it implied would be one no
// operator should run.
//
// On Windows there are no POSIX mode bits to read: Go synthesises a mode from
// the read-only attribute, so every writable file reports 0666 and every
// directory 0777. The check would refuse every store on that platform while
// carrying no information, so it does not run there.
func CheckPermissions(path string) ([]PermissionIssue, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}

	issues := make([]PermissionIssue, 0, 3)
	for i, p := range []string{path, path + "-wal", path + "-shm"} {
		// Lstat, not Stat, for the sidecars: a sidecar replaced by a link
		// reports its target's mode, so following the link would clear a store
		// whose -wal contents live wherever the link leads. SQLite guards the
		// same case, opening -shm with O_NOFOLLOW. The database itself is
		// followed, since pointing it at another path is an operator's choice.
		isSidecar := i > 0
		info, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("inspecting %s: %w", p, err)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if isSidecar {
				issues = append(issues, PermissionIssue{Path: p, Mode: info.Mode(), Symlink: true})
				continue
			}
			info, err = os.Stat(p)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("inspecting %s: %w", p, err)
			}
		}

		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			issues = append(issues, PermissionIssue{Path: p, Mode: perm})
		}
	}
	return issues, nil
}
