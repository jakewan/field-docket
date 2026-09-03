package server

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// validRecord is the minimal accepted call. Specs vary one field at a time from
// this baseline so a failure names the field under test.
func validRecord() map[string]any {
	return map[string]any{
		"observation": "The backoff resets on every retry rather than accumulating.",
		"class":       "correctness",
		"scope_ref":   "jakewan/field-docket",
	}
}

func withField(base map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(base)+1)
	maps.Copy(out, base)
	if value == nil {
		delete(out, key)
	} else {
		out[key] = value
	}
	return out
}

func TestServerReportsIdentity(t *testing.T) {
	cs := newTestSession(t)

	got := cs.InitializeResult().ServerInfo
	if got.Name != serverName {
		t.Errorf("server name = %q, want %q", got.Name, serverName)
	}
	if got.Title != serverTitle {
		t.Errorf("server title = %q, want %q", got.Title, serverTitle)
	}
	// Asserted against the variable, not a literal: the value differs between a
	// plain build and a release build. This does not guard the ldflags
	// injection — see the serverVersion doc comment.
	if got.Version != serverVersion {
		t.Errorf("server version = %q, want %q", got.Version, serverVersion)
	}
}

func TestServerExposesRecordAndReviewTools(t *testing.T) {
	cs := newTestSession(t)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	got := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}

	for _, want := range []string{"record_observation", "review_observations"} {
		if !got[want] {
			t.Errorf("tool %q is not exposed", want)
		}
	}
	if len(res.Tools) != 2 {
		t.Errorf("exposed %d tools, want exactly 2: %v", len(res.Tools), got)
	}
}

func TestRecordEchoesPersistedIdentity(t *testing.T) {
	cs := newTestSession(t)

	var out struct {
		ID         string `json:"id"`
		RecordedAt string `json:"recorded_at"`
	}
	decodeStructured(t, callTool(t, cs, "record_observation", validRecord()), &out)

	if out.ID == "" {
		t.Error("id is empty")
	}
	// The test clock starts at fixedStart, so the stamp is fully determined.
	want := "2026-03-14T09:26:53.000Z"
	if out.RecordedAt != want {
		t.Errorf("recorded_at = %q, want %q", out.RecordedAt, want)
	}
}

func TestRecordRejectsMissingObservationOrClass(t *testing.T) {
	cs := newTestSession(t)

	for _, field := range []string{"observation", "class"} {
		t.Run("missing_"+field, func(t *testing.T) {
			res := callTool(t, cs, "record_observation", withField(validRecord(), field, nil))
			if !res.IsError {
				t.Fatalf("omitting %q was accepted; the schema should reject it", field)
			}
		})
	}
}

func TestRecordRejectsBlankObservationOrClass(t *testing.T) {
	cs := newTestSession(t)

	// Whitespace-only passes schema validation — the field is present and a
	// string — so this is the handler's trim check, not the schema's.
	for _, field := range []string{"observation", "class"} {
		t.Run("blank_"+field, func(t *testing.T) {
			res := callTool(t, cs, "record_observation", withField(validRecord(), field, "   "))
			if !res.IsError {
				t.Fatalf("a whitespace-only %q was accepted", field)
			}
		})
	}
}

func TestRecordDefaultsScopeToProject(t *testing.T) {
	cs := newTestSession(t)

	// scope_kind is omitted entirely; the schema's published default supplies it.
	res := callTool(t, cs, "record_observation", withField(validRecord(), "scope_kind", nil))
	if res.IsError {
		t.Fatalf("record failed: %s", errorText(t, res))
	}

	got := review(t, cs, nil)
	if len(got.Observations) != 1 {
		t.Fatalf("returned %d observations, want 1", len(got.Observations))
	}
	if got.Observations[0].ScopeKind != "project" {
		t.Errorf("scope_kind = %q, want %q", got.Observations[0].ScopeKind, "project")
	}
}

func TestRecordRequiresScopeRefForProjectScope(t *testing.T) {
	cs := newTestSession(t)

	res := callTool(t, cs, "record_observation", withField(validRecord(), "scope_ref", nil))
	if !res.IsError {
		t.Fatal("a project-scoped record with no scope_ref was accepted; " +
			"it would be filed unrecoverably in an append-only store")
	}
	if !strings.Contains(errorText(t, res), "scope_ref") {
		t.Errorf("error does not name the offending field: %s", errorText(t, res))
	}
}

func TestRecordAcceptsUserScopeWithoutScopeRef(t *testing.T) {
	cs := newTestSession(t)

	args := withField(validRecord(), "scope_ref", nil)
	args["scope_kind"] = "user"

	res := callTool(t, cs, "record_observation", args)
	if res.IsError {
		t.Fatalf("user-scoped record without scope_ref was refused: %s", errorText(t, res))
	}
}

func TestRecordRejectsUnknownScopeKind(t *testing.T) {
	cs := newTestSession(t)

	// Rejected by the published enum in the tool schema, before the handler runs.
	res := callTool(t, cs, "record_observation", withField(validRecord(), "scope_kind", "galaxy"))
	if !res.IsError {
		t.Fatal("an unrecognised scope_kind was accepted")
	}
}

func TestRecordAttributesTheCallingClient(t *testing.T) {
	cs := newTestSession(t)

	if res := callTool(t, cs, "record_observation", validRecord()); res.IsError {
		t.Fatalf("record failed: %s", errorText(t, res))
	}

	got := review(t, cs, nil)
	if len(got.Observations) != 1 {
		t.Fatalf("returned %d observations, want 1", len(got.Observations))
	}
	// connect dials as "test-client"; the value comes from the handshake rather
	// than from anything the caller passed.
	if got.Observations[0].Agent != "test-client" {
		t.Errorf("agent = %q, want %q", got.Observations[0].Agent, "test-client")
	}
}

func TestClientNameToleratesAMissingHandshake(t *testing.T) {
	// Both hops are nilable, and a panic here would tear down the session rather
	// than failing one call. Driving this through a real client is not possible —
	// the SDK always sends ClientInfo — so the guard is exercised directly.
	if got := clientName(nil); got != "" {
		t.Errorf("clientName(nil) = %q, want empty", got)
	}
	if got := clientName(&mcp.CallToolRequest{}); got != "" {
		t.Errorf("clientName with no session = %q, want empty", got)
	}
}

func TestRecordNormalizesClassAndScopeRef(t *testing.T) {
	cs := newTestSession(t)

	args := validRecord()
	args["class"] = "  Correctness  "
	args["scope_ref"] = "  JakeWan/Field-Docket  "

	if res := callTool(t, cs, "record_observation", args); res.IsError {
		t.Fatalf("record failed: %s", errorText(t, res))
	}

	got := review(t, cs, nil)
	if len(got.Observations) != 1 {
		t.Fatalf("returned %d observations, want 1", len(got.Observations))
	}
	// Normalised rather than rejected. The store is append-only, so a vocabulary
	// fragmented by case or stray whitespace cannot be consolidated afterwards.
	if got.Observations[0].Class != "correctness" {
		t.Errorf("class = %q, want %q", got.Observations[0].Class, "correctness")
	}
	if got.Observations[0].ScopeRef != "jakewan/field-docket" {
		t.Errorf("scope_ref = %q, want %q", got.Observations[0].ScopeRef, "jakewan/field-docket")
	}
}

// TestRecordRefusesOnlyMalformedInput pins the load-bearing invariant:
// recording is separated from adjudication, so nothing the docket already
// contains can cause a write to be refused.
//
// The assertion is the completeness of the table, and the accept cases are what
// give it teeth. A refusal-only enumeration would still pass after a
// state-derived refusal were added, because the new refusal would fire on inputs
// this table never exercises. Asserting that these inputs are *accepted* turns
// any such addition into a failure.
func TestRecordRefusesOnlyMalformedInput(t *testing.T) {
	tests := []struct {
		name       string
		args       map[string]any
		wantRefuse bool
	}{
		// The complete refusal set.
		{"blank observation", withField(validRecord(), "observation", "  "), true},
		{"missing observation", withField(validRecord(), "observation", nil), true},
		{"blank class", withField(validRecord(), "class", "  "), true},
		{"missing class", withField(validRecord(), "class", nil), true},
		{"unknown scope_kind", withField(validRecord(), "scope_kind", "galaxy"), true},
		{"project scope without scope_ref", withField(validRecord(), "scope_ref", nil), true},

		// Everything else is accepted. These are the cases a state-derived
		// refusal would break.
		{"baseline", validRecord(), false},
		{"user scope without scope_ref", map[string]any{
			"observation": "Guidance conflicted with itself across two rules.",
			"class":       "consistency",
			"scope_kind":  "user",
		}, false},
		{"novel class", withField(validRecord(), "class", "some-class-never-seen-before"), false},
		{"duplicate of an existing observation", validRecord(), false},
		{"with a subject", withField(validRecord(), "subject", "internal/store/store.go"), false},
		{"unusually long observation", withField(validRecord(), "observation",
			strings.Repeat("a very long observation. ", 200)), false},
	}

	// One store across every case, so later cases run against a docket that
	// already holds the earlier ones. That is what makes "no stored state gates a
	// write" an assertion rather than a claim about an empty database.
	cs := newTestSession(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, cs, "record_observation", tt.args)
			switch {
			case tt.wantRefuse && !res.IsError:
				t.Errorf("input was accepted but should be refused as malformed")
			case !tt.wantRefuse && res.IsError:
				t.Errorf("input was refused: %s\n"+
					"Only malformed input may be refused. If a refusal was added that "+
					"depends on what the docket already contains, that breaks the "+
					"recording/adjudication separation.", errorText(t, res))
			}
		})
	}
}

func TestRecordSucceedsAgainstAPopulatedStore(t *testing.T) {
	cs := newTestSession(t)

	// Seed across every scope and a spread of classes, so any store-contents
	// dependence on the write path has something to trip over.
	classes := []string{"correctness", "diagnostics", "ergonomics", "consistency"}
	for i := range 200 {
		args := map[string]any{
			"observation": "Seeded observation for the populated-store check.",
			"class":       classes[i%len(classes)],
		}
		if i%2 == 0 {
			args["scope_kind"] = "user"
		} else {
			args["scope_ref"] = "jakewan/field-docket"
		}
		if res := callTool(t, cs, "record_observation", args); res.IsError {
			t.Fatalf("seeding record %d failed: %s", i, errorText(t, res))
		}
	}

	res := callTool(t, cs, "record_observation", validRecord())
	if res.IsError {
		t.Fatalf("record against a populated store failed: %s", errorText(t, res))
	}

	got := review(t, cs, map[string]any{"limit": 1})
	if got.Total != 201 {
		t.Errorf("total = %d, want 201", got.Total)
	}
}

func TestRecordToleratesNullArguments(t *testing.T) {
	cs := newTestSession(t)

	// A literal JSON null payload. Without the tolerateNullArguments middleware
	// the SDK nils the arguments map and jsonschema-go panics applying
	// scope_kind's default into it, tearing down the whole session.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "record_observation",
		Arguments: json.RawMessage("null"),
	})
	if err != nil {
		t.Fatalf("null arguments tore down the call: %v", err)
	}
	// Refused for missing required fields — the point is that it refused rather
	// than panicked, and that the session survives.
	if !res.IsError {
		t.Error("null arguments were accepted as a valid record")
	}

	if res := callTool(t, cs, "record_observation", validRecord()); res.IsError {
		t.Fatalf("session is unusable after a null-arguments call: %s", errorText(t, res))
	}
}
