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

	st, err := openStore(ctx, configPath)
	if err != nil {
		log.Fatalf("field-docket: %v", err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("field-docket: closing store: %v", cerr)
		}
	}()

	if rerr := run(ctx, server.New(st), &mcp.StdioTransport{}); rerr != nil {
		log.Fatalf("mcp server: %v", rerr)
	}
}

// openStore resolves the store location and opens it.
//
// Config supplies the path when it names one; otherwise the XDG default
// applies. Resolving that default lives here rather than in the config package
// so config stays ignorant of the store.
func openStore(ctx context.Context, configPath string) (*store.Store, error) {
	cfg, err := config.Load(ctx, configPath)
	if err != nil {
		return nil, err
	}

	path := cfg.Store
	if path == "" {
		path, err = store.DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	st, err := store.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return st, nil
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

	st, err := openStore(ctx, *configPath)
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

// parseConfigFlag extracts --config from args.
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
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == codeServerClosing
}
