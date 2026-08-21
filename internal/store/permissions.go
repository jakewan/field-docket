package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"
)

// PermissionIssue names one store file reachable by someone other than its
// owner. Symlink distinguishes the two ways that happens, because the remedy
// differs: a loose mode is repaired with chmod, a replaced sidecar is not.
type PermissionIssue struct {
	Path string

	// Mode holds the file's permission bits, and is zero when Symlink is set —
	// a link's own mode says nothing about what it exposes, and the type bits
	// that would otherwise ride along here render as nonsense in octal.
	Mode fs.FileMode

	Symlink bool
}

// String renders an issue for an operator-facing message.
func (p PermissionIssue) String() string {
	if p.Symlink {
		return fmt.Sprintf("%s is a symbolic link", p.Path)
	}
	return fmt.Sprintf("%s has mode %04o", p.Path, p.Mode.Perm())
}

// UnsafeStoreError explains why a store carrying these issues is not served,
// and what to do about it.
//
// The message has to carry three things, because the operator sees it in a tool
// result with nothing else around it. Which files are wrong, so the finding can
// be checked. What refusing means — not merely that someone could read the
// docket, but that something could have written to it, which is what decides
// whether the record already stored can be trusted. And two ways forward, since
// repairing the modes and deciding the corpus is sound are different judgments:
// the chmod, and the snapshot that captures the store as it stands for
// examination first.
func UnsafeStoreError(issues []PermissionIssue) error {
	var b strings.Builder
	b.WriteString("this docket is not being served because ")
	if len(issues) == 1 {
		b.WriteString("one of its files is reachable by more than its owner: ")
	} else {
		b.WriteString("some of its files are reachable by more than their owner: ")
	}
	for i, issue := range issues {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(issue.String())
	}

	// The modes say something happened, not what. This server creates the docket
	// 0600, so any other mode means something outside it touched the files — and
	// a mode describes only the present, so one that is 0644 now may have been
	// 0666 an hour ago. Since the append-only triggers bind callers coming
	// through this server rather than anything holding the file, that is enough
	// to put the stored record in question.
	b.WriteString(". This server creates a docket 0600, so another mode means something " +
		"outside it has touched these files, and a mode carries no history of what it was " +
		"before. The record may have been modified, not merely read, so the observations " +
		"already stored cannot be assumed intact.")

	var repair []string
	var links []string
	for _, issue := range issues {
		if issue.Symlink {
			links = append(links, issue.Path)
			continue
		}
		repair = append(repair, shellQuote(issue.Path))
	}

	// One command, ending the sentence before the paths so no punctuation abuts
	// one. An operator mid-incident pastes this; a remedy that looks runnable
	// and is not costs more than none, because they act on it, part of it fails,
	// and the next refusal names a file they believe they repaired.
	if len(repair) > 0 {
		fmt.Fprintf(&b, " To repair the permissions and start again, run:\n    chmod 0600 %s\n",
			strings.Join(repair, " "))
	}
	for _, link := range links {
		fmt.Fprintf(&b, " %s is a symbolic link and needs replacing by hand with the file it "+
			"stands in for; changing its mode would affect whatever it points at instead.\n", link)
	}

	// Deliberately not offered as a way to capture the docket untouched: taking
	// one opens the store, which creates its -wal/-shm sidecars beside it, and
	// on a docket written by an older build would bring the schema forward. An
	// operator wanting an unaltered image should copy the files first.
	b.WriteString("To copy the docket before touching it, copy the database and any " +
		"-wal/-shm files beside it; `field-docket snapshot <path>` also works, but it opens " +
		"the docket to do so. To keep using this docket as it is, list its path under " +
		"allow_unsafe_permissions in the config file.")

	return errors.New(b.String())
}

// shellQuote renders path as a single shell word.
//
// The paths come from a config file and from the filesystem, so a space or a
// quote in one is the operator's business rather than an error — but it would
// silently split the repair command into the wrong arguments, and this string
// exists to be pasted.
func shellQuote(path string) string {
	if !strings.ContainsAny(path, " \t\n'\"\\$`*?[]{}()|&;<>#~!") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
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

	// -journal belongs here even though this server never produces one: it runs
	// in WAL mode, but the premise of the check is that something else touched
	// the files, and an out-of-band sqlite3 session in the default rollback mode
	// leaves a -journal holding page images of the store's contents. Keep the
	// database first — the sidecar branch below is keyed on position.
	issues := make([]PermissionIssue, 0, 4)
	for i, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
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
				issues = append(issues, PermissionIssue{Path: p, Symlink: true})
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
