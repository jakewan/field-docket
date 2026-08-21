// Command field-docket serves the observation docket over MCP on stdio.
//
// It also carries one non-MCP subcommand, `snapshot`, because a backup tool
// copies files and cannot run SQL — see runSnapshot.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/jakewan/field-docket/internal/config"
	"github.com/jakewan/field-docket/internal/server"
	"github.com/jakewan/field-docket/internal/store"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// codeServerClosing is the JSON-RPC error the SDK reports when the peer goes
// away during shutdown. It is not documented as an exported constant, so it is
// named here rather than compared as a bare number at the call site.
const codeServerClosing = -32004

func main() {
	ctx := context.Background()

	// The snapshot subcommand is dispatched before flag parsing so its own
	// argument is not mistaken for a server flag.
	if len(os.Args) > 1 && os.Args[1] == "snapshot" {
		if err := runSnapshot(ctx, os.Args[2:]); err != nil {
			log.Fatalf("field-docket snapshot: %v", err)
		}
		return
	}

	configPath, err := parseConfigFlag(os.Args[1:])
	if err != nil {
		log.Fatalf("field-docket: %v", err)
	}

	target, err := resolveStore(ctx, configPath)
	if err != nil {
		log.Fatalf("field-docket: %v", err)
	}

	// A store that should not be served is not opened at all, and the server is
	// started anyway so it can say why. Exiting here instead would put the whole
	// explanation on stderr, which a client captures to a log file — leaving the
	// calling agent to see nothing but two missing tools.
	var st *store.Store
	if target.unusable != nil {
		log.Printf("field-docket: %v", target.unusable)
	} else {
		st, err = store.Open(ctx, target.path)
		if err != nil {
			log.Fatalf("field-docket: %v", err)
		}
		defer func() {
			if cerr := st.Close(); cerr != nil {
				log.Printf("field-docket: closing store: %v", cerr)
			}
		}()
	}

	if rerr := run(ctx, server.New(st, target.unusable), &mcp.StdioTransport{}); rerr != nil {
		log.Fatalf("mcp server: %v", rerr)
	}
}

// storeTarget is where the store lives, and why it should not be served if it
// should not be.
type storeTarget struct {
	path string

	// unusable is set when the store's files are reachable beyond their owner
	// and the operator has not said that is acceptable for this store. It is a
	// reason to refuse rather than a reason to stop: the two entry points do
	// different things with it.
	unusable error
}

// resolveStore resolves the store location and decides whether it can be served.
//
// Config supplies the path when it names one; otherwise the XDG default
// applies. Resolving that default lives here rather than in the config package
// so config stays ignorant of the store.
//
// The permission check runs before anything opens the store, which is what lets
// the serve path decline without touching it — opening in WAL mode creates the
// -wal and -shm sidecars, and a store whose provenance is in question should not
// be altered by the program that noticed.
func resolveStore(ctx context.Context, configPath string) (storeTarget, error) {
	cfg, err := config.Load(ctx, configPath)
	if err != nil {
		return storeTarget{}, err
	}

	path := cfg.Store
	if path == "" {
		path, err = store.DefaultPath()
		if err != nil {
			return storeTarget{}, err
		}
	}

	issues, err := store.CheckPermissions(path)
	if err != nil {
		return storeTarget{}, err
	}
	if len(issues) == 0 {
		return storeTarget{path: path}, nil
	}

	exempt, err := allowlisted(path, cfg.AllowUnsafePermissions)
	if err != nil {
		return storeTarget{}, err
	}
	if exempt {
		return storeTarget{path: path}, nil
	}
	return storeTarget{path: path, unusable: store.UnsafeStoreError(issues)}, nil
}

// allowlisted reports whether any entry names the same file as path.
//
// Compared as paths rather than as strings. The two spellings reach this from
// different directions — the store path from the config's store key or the XDG
// default, the entry typed by an operator reading a refusal — so requiring them
// to match character for character would make an exemption fail on a relative
// spelling against an absolute one, silently. That is the worst way for the one
// mechanism that exists for when this guard is wrong to break.
//
// Resolved to absolute (which also cleans) but not through symbolic links: a
// store reached by a link would still need its own entry. Following links would
// mean failing on a path that does not resolve, which is a new way for the
// escape hatch to stop working in exchange for a case nothing has asked for.
func allowlisted(path string, entries []string) (bool, error) {
	if len(entries) == 0 {
		return false, nil
	}
	want, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolving store path %s: %w", path, err)
	}
	for _, entry := range entries {
		got, aerr := filepath.Abs(entry)
		if aerr != nil {
			return false, fmt.Errorf("resolving allow_unsafe_permissions entry %s: %w", entry, aerr)
		}
		if got == want {
			return true, nil
		}
	}
	return false, nil
}

// runSnapshot writes a consistent copy of the store to the given destination.
//
// This exists because the store is backed up by a file-copying tool, and copying
// a live WAL database can capture a mid-transaction state that will not open. A
// snapshot is a single settled file with no sidecars, so the backup is
// restorable.
func runSnapshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the config file")
	fs.Usage = func() {
		// If the usage stream is unwritable there is nothing useful to do about
		// it and nothing gained by printing the flag defaults into it either, so
		// the error short-circuits rather than being discarded.
		if _, werr := fmt.Fprintln(fs.Output(), "usage: field-docket snapshot [--config path] <destination>"); werr != nil {
			return
		}
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("expected exactly one destination path")
	}

	target, err := resolveStore(ctx, *configPath)
	if err != nil {
		return err
	}
	// Unlike the serve path, a snapshot of a suspect store still runs. Capturing
	// it is how an operator gets a copy to examine before deciding whether to
	// trust the record, and the copy is written at 0600 regardless of the
	// source's mode — so this is the repair path, not a way to spread the
	// problem. Warning here reaches someone: this subcommand is run from a
	// terminal, where stderr is read.
	//
	// Its own wording rather than the served message, which offers taking a
	// snapshot as the way forward — advice that reads as a loop to someone who
	// is already taking one.
	if target.unusable != nil {
		log.Printf("field-docket snapshot: %s is reachable by more than its owner, "+
			"so the record it holds may have been modified; this copy is consistent but not untouched: taking it opens the docket",
			target.path)
	}

	st, err := store.Open(ctx, target.path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("field-docket: closing store: %v", cerr)
		}
	}()

	return st.Snapshot(ctx, fs.Arg(0))
}

// parseConfigFlag extracts --config from args, rejecting anything left over.
//
// A leftover argument is how a mistyped subcommand arrives: it does not match
// the snapshot dispatch in main, so it falls through to here, and accepting it
// would start a server instead — doing nothing the operator asked for, and
// saying nothing about it.
//
// It uses its own FlagSet rather than the package-level flag functions so this
// stays a pure function over its arguments and can be exercised directly,
// without a test mutating global flag state or os.Args.
func parseConfigFlag(args []string) (string, error) {
	fs := flag.NewFlagSet("field-docket", flag.ContinueOnError)
	path := fs.String("config", "", "path to the config file")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected argument %q (the only subcommand is snapshot)", fs.Arg(0))
	}
	return *path, nil
}

// run serves until the peer disconnects, treating a clean shutdown as success.
func run(ctx context.Context, srv *mcp.Server, transport mcp.Transport) error {
	if err := srv.Run(ctx, transport); err != nil && !isCleanShutdown(err) {
		return err
	}
	return nil
}

// isCleanShutdown reports whether err is the ordinary end of a session rather
// than a failure.
//
// An MCP client closes stdin when it is done, which surfaces as EOF or as the
// SDK's server-closing JSON-RPC error. Treating either as a failure would make
// every normal exit log an error.
func isCleanShutdown(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	wire, ok := errors.AsType[*jsonrpc.Error](err)
	return ok && wire.Code == codeServerClosing
}
