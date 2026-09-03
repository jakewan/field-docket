package server

import (
	"errors"
	"strings"
	"testing"
)

// TestUnavailableStoreRefusesBothTools pins that the refusal reaches the caller
// as a tool error rather than a dead process.
//
// The channel is the point. Failing at startup would put the whole explanation
// on stderr, which an MCP client captures to a log file nobody reads during a
// session — the calling agent would see only that the tools had vanished. A tool
// error is surfaced to whoever is driving the session, which is the only place
// the operator can act on it.
//
// Both tools refuse, not only the one that writes. Permissions loose enough for
// another process to read the store are loose enough for one to have written to
// it, and the append-only triggers bind callers going through this server rather
// than anything holding the file — DROP TRIGGER is ordinary SQL. A review served
// from a store in that condition would present evidence of unknown provenance as
// though it were sound.
//
// The reason is relayed whole rather than summarised: this package does not
// compose it and must not paraphrase it, or the operator loses the half that
// says what to do. See store.UnsafeStoreError for the message itself.
func TestUnavailableStoreRefusesBothTools(t *testing.T) {
	reason := errors.New("a store this server will not serve, and what to do about it")
	cs := connect(t, New(nil, reason))

	// Arguments each tool's schema accepts. The SDK validates against the input
	// schema before the handler runs, so a call that is merely wrong for the
	// tool reports a schema error and never reaches the refusal under test.
	for tool, args := range map[string]map[string]any{
		"record_observation": {
			"observation": "An observation.",
			"class":       "correctness",
			"scope_ref":   "owner/repo",
		},
		"review_observations": {},
	} {
		res := callTool(t, cs, tool, args)
		if !res.IsError {
			t.Fatalf("%s served an unavailable store, want a tool error", tool)
		}
		if got := errorText(t, res); !strings.Contains(got, reason.Error()) {
			t.Errorf("%s reported %q, want it to carry %q", tool, got, reason)
		}
	}
}

// TestUnavailableStoreDoesNotTouchTheStore is the guard behind passing a nil
// store: the handlers must refuse before reaching for it. If a later edit moved
// the check below the point where a handler dereferences d.store, this panics
// rather than quietly serving.
//
// It calls each tool with input the ordinary path would reject, so a refusal
// that came from validation rather than from the unavailability would still have
// had to run past the nil store to get there.
func TestUnavailableStoreDoesNotTouchTheStore(t *testing.T) {
	reason := errors.New("unavailable")
	cs := connect(t, New(nil, reason))

	// Input each schema accepts but each handler would otherwise reject: a blank
	// observation, and a paging cursor missing its other half. If the refusal
	// ever moved below those validations the handler would reach a nil store, so
	// this fails loudly rather than passing on a coincidentally similar message.
	for tool, args := range map[string]map[string]any{
		"record_observation":  {"observation": "   ", "class": "   "},
		"review_observations": {"before_id": "dangling-cursor-with-no-before"},
	} {
		res := callTool(t, cs, tool, args)
		if !res.IsError {
			t.Fatalf("%s did not report the unavailable store", tool)
		}
		if got := errorText(t, res); !strings.Contains(got, reason.Error()) {
			t.Errorf("%s reported %q, want the unavailability to win over input validation", tool, got)
		}
	}
}
