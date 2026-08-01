package main

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseConfigFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"absent", nil, ""},
		{"separate value", []string{"--config", "/tmp/config.yml"}, "/tmp/config.yml"},
		{"joined value", []string{"--config=/tmp/config.yml"}, "/tmp/config.yml"},
		{"single dash", []string{"-config", "/tmp/config.yml"}, "/tmp/config.yml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseConfigFlag(tt.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tt.want {
				t.Errorf("config = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseConfigFlagRejectsUnknownFlags(t *testing.T) {
	if _, err := parseConfigFlag([]string{"--nonsense"}); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}

// TestParseConfigFlagRejectsPositionalArguments covers the operator-mistake case
// the snapshot dispatch in main lets through: a mistyped subcommand does not
// match "snapshot", so it arrives here, and a parser that ignores leftover
// arguments would start a server instead — doing nothing the operator asked for
// and saying nothing about it. runSnapshot already rejects a wrong argument
// count; this holds the server path to the same contract.
func TestParseConfigFlagRejectsPositionalArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"mistyped subcommand", []string{"snapshto", "/tmp/snapshot.db"}},
		{"stray argument after a valid flag", []string{"--config", "/tmp/config.yml", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseConfigFlag(tt.args); err == nil {
				t.Fatalf("positional arguments %q were accepted", tt.args)
			}
		})
	}
}

// TestIsCleanShutdown covers the exits that are not failures. An MCP client
// closes stdin when it is done, and treating that as an error would make every
// normal session end in a fatal log line.
func TestIsCleanShutdown(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, true},
		{"peer closed stdin", io.EOF, true},
		{"wrapped EOF", errors.Join(errors.New("reading"), io.EOF), true},
		{"server closing", &jsonrpc.Error{Code: codeServerClosing}, true},
		{"other jsonrpc error", &jsonrpc.Error{Code: -32600}, false},
		{"unrelated failure", errors.New("disk on fire"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCleanShutdown(tt.err); got != tt.want {
				t.Errorf("isCleanShutdown(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// nopWriteCloser adapts a Writer for IOTransport, which wants a WriteCloser.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// TestRunReturnsNilOnPeerDisconnect drives the composition root's serve loop
// against a peer that disconnects mid-traffic — stdin reaching EOF with requests
// in flight, which is exactly how a client shuts a session down. The pipe stands
// in for stdin; the contract under test is the shutdown classification, not tool
// behaviour, so the server carries no store.
func TestRunReturnsNilOnPeerDisconnect(t *testing.T) {
	ctx := context.Background()
	pr, pw := io.Pipe()
	transport := &mcp.IOTransport{Reader: pr, Writer: nopWriteCloser{io.Discard}}

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)

	done := make(chan error, 1)
	go func() { done <- run(ctx, srv, transport) }()

	handshake := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"
	if _, err := io.WriteString(pw, handshake); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	if err := pw.Close(); err != nil { // peer disconnects (EOF) mid-traffic
		t.Fatalf("close pipe: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run() after peer disconnect = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return after peer disconnect")
	}
}
