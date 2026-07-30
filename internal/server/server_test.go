package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jakewan/field-docket/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fixedStart is the instant the test clock begins at. It is deliberately not
// UTC-midnight and not the local zone, so a spec that accidentally depends on
// either shows up as a failure rather than passing by coincidence.
var fixedStart = time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)

// advancingClock returns a clock that moves forward by step on every call.
//
// Tests must not rely on the wall clock for ordering. Two observations recorded
// in the same millisecond tie on recorded_at, and the id tiebreak is random
// within that millisecond (UUIDv7 leaves the sub-millisecond bits random), so
// "most recent first" would be genuinely undefined rather than merely flaky.
func advancingClock(step time.Duration) func() time.Time {
	current := fixedStart
	return func() time.Time {
		t := current
		current = current.Add(step)
		return t
	}
}

// newTestStore opens a store isolated under t.TempDir() with a deterministic
// clock, so specs never touch the real XDG state directory.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "field-docket.db"),
		store.WithClock(advancingClock(time.Second)),
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("store close: %v", cerr)
		}
	})
	return st
}

// connect stands up the server over an in-memory transport and returns a
// connected client session, registering cleanup for both ends. Every behavior
// spec needs a live client/server pair and the wiring is identical across them.
func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() {
		if cerr := serverSession.Close(); cerr != nil {
			t.Errorf("server session close: %v", cerr)
		}
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		if cerr := clientSession.Close(); cerr != nil {
			t.Errorf("client session close: %v", cerr)
		}
	})
	return clientSession
}

// newTestSession is the common setup: a temp-dir store behind a connected
// client session.
func newTestSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return connect(t, New(newTestStore(t)))
}

// callTool invokes a tool and fails the test if the call itself errored.
// A tool-level error (res.IsError) is returned to the caller to assert on.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// errorText pulls the human-readable message out of a tool-error result.
func errorText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content in tool error, got %T", res.Content[0])
	}
	return tc.Text
}

// decodeStructured round-trips a result's structured output into out. The SDK
// hands back a decoded value rather than raw JSON, so re-marshalling is how a
// spec asserts against the wire shape the client actually sees.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned error: %s", errorText(t, res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
}

// reviewResult mirrors review_observations' output for assertions.
type reviewResult struct {
	Observations []struct {
		ID          string `json:"id"`
		RecordedAt  string `json:"recorded_at"`
		Class       string `json:"class"`
		Observation string `json:"observation"`
		ScopeKind   string `json:"scope_kind"`
		ScopeRef    string `json:"scope_ref"`
		Subject     string `json:"subject"`
		Agent       string `json:"agent"`
	} `json:"observations"`
	ClassCounts []struct {
		Class string `json:"class"`
		Count int    `json:"count"`
	} `json:"class_counts"`
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

func review(t *testing.T, cs *mcp.ClientSession, args map[string]any) reviewResult {
	t.Helper()
	var out reviewResult
	decodeStructured(t, callTool(t, cs, "review_observations", args), &out)
	return out
}

// TestAcceptanceRecordThenReview drives the whole chain end to end: MCP stdio
// contract, tool handler, SQLite store, and read-back. It is the spec the
// implementation is grown against, and stays red until every layer exists.
func TestAcceptanceRecordThenReview(t *testing.T) {
	cs := newTestSession(t)

	first := callTool(t, cs, "record_observation", map[string]any{
		"observation": "The retry budget is consumed before the first backoff.",
		"class":       "correctness",
		"scope_ref":   "jakewan/field-docket",
	})
	if first.IsError {
		t.Fatalf("first record failed: %s", errorText(t, first))
	}

	second := callTool(t, cs, "record_observation", map[string]any{
		"observation": "Config errors name the field but not the file.",
		"class":       "diagnostics",
		"scope_ref":   "jakewan/field-docket",
	})
	if second.IsError {
		t.Fatalf("second record failed: %s", errorText(t, second))
	}

	var recorded struct {
		ID         string `json:"id"`
		RecordedAt string `json:"recorded_at"`
	}
	decodeStructured(t, first, &recorded)
	if recorded.ID == "" {
		t.Error("record_observation returned an empty id")
	}
	if recorded.RecordedAt == "" {
		t.Error("record_observation returned an empty recorded_at")
	}

	got := review(t, cs, nil)

	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
	if got.Truncated {
		t.Error("truncated = true, want false: two observations fit under the default limit")
	}
	if len(got.Observations) != 2 {
		t.Fatalf("returned %d observations, want 2", len(got.Observations))
	}

	// Most recent first: the second observation was recorded a tick later.
	if got.Observations[0].Class != "diagnostics" {
		t.Errorf("first result class = %q, want %q (most recent first)",
			got.Observations[0].Class, "diagnostics")
	}
	if got.Observations[1].Class != "correctness" {
		t.Errorf("second result class = %q, want %q", got.Observations[1].Class, "correctness")
	}

	// Both observations carry the scope they were recorded with, and the
	// default scope_kind applied without being passed.
	for i, obs := range got.Observations {
		if obs.ScopeKind != "project" {
			t.Errorf("observation %d scope_kind = %q, want %q (the default)", i, obs.ScopeKind, "project")
		}
		if obs.ScopeRef != "jakewan/field-docket" {
			t.Errorf("observation %d scope_ref = %q, want %q", i, obs.ScopeRef, "jakewan/field-docket")
		}
	}

	if len(got.ClassCounts) != 2 {
		t.Fatalf("class_counts has %d entries, want 2", len(got.ClassCounts))
	}
	for _, cc := range got.ClassCounts {
		if cc.Count != 1 {
			t.Errorf("class %q count = %d, want 1", cc.Class, cc.Count)
		}
	}
}
