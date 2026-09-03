package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jakewan/field-docket/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionWithClock builds a session whose store uses the supplied clock, for
// specs that need to control timestamps rather than merely advance them.
func sessionWithClock(t *testing.T, now func() time.Time) *mcp.ClientSession {
	t.Helper()
	st, err := store.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "field-docket.db"),
		store.WithClock(now),
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("store close: %v", cerr)
		}
	})
	return connect(t, New(st, nil))
}

// seed records n observations of the given class, failing on any refusal.
func seed(t *testing.T, cs *mcp.ClientSession, n int, class string) {
	t.Helper()
	for i := range n {
		res := callTool(t, cs, "record_observation", map[string]any{
			"observation": fmt.Sprintf("Seeded observation %d.", i),
			"class":       class,
			"scope_ref":   "jakewan/field-docket",
		})
		if res.IsError {
			t.Fatalf("seeding %s %d: %s", class, i, errorText(t, res))
		}
	}
}

func TestReviewReturnsMostRecentFirst(t *testing.T) {
	cs := newTestSession(t)
	seed(t, cs, 3, "ordering")

	got := review(t, cs, nil)
	if len(got.Observations) != 3 {
		t.Fatalf("returned %d observations, want 3", len(got.Observations))
	}
	for i := 1; i < len(got.Observations); i++ {
		if got.Observations[i-1].RecordedAt < got.Observations[i].RecordedAt {
			t.Errorf("observation %d is older than %d; results are not newest-first", i-1, i)
		}
	}
}

func TestReviewFiltersByClass(t *testing.T) {
	cs := newTestSession(t)
	seed(t, cs, 2, "correctness")
	seed(t, cs, 3, "diagnostics")

	got := review(t, cs, map[string]any{"class": "diagnostics"})
	if got.Total != 3 {
		t.Errorf("total = %d, want 3", got.Total)
	}
	for _, obs := range got.Observations {
		if obs.Class != "diagnostics" {
			t.Errorf("class %q leaked through the filter", obs.Class)
		}
	}
}

func TestReviewFiltersByScope(t *testing.T) {
	cs := newTestSession(t)

	record := func(kind, ref string) {
		t.Helper()
		args := map[string]any{
			"observation": "Scoped observation.",
			"class":       "scoping",
			"scope_kind":  kind,
		}
		if ref != "" {
			args["scope_ref"] = ref
		}
		if res := callTool(t, cs, "record_observation", args); res.IsError {
			t.Fatalf("record: %s", errorText(t, res))
		}
	}
	record("project", "jakewan/field-docket")
	record("project", "owner/other-repo")
	record("user", "")
	record("user", "")

	tests := []struct {
		name  string
		args  map[string]any
		total int
	}{
		{"by kind user", map[string]any{"scope_kind": "user"}, 2},
		{"by kind project", map[string]any{"scope_kind": "project"}, 2},
		{"by ref", map[string]any{"scope_ref": "owner/other-repo"}, 1},
		{"kind and ref together", map[string]any{
			"scope_kind": "project", "scope_ref": "jakewan/field-docket"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := review(t, cs, tt.args); got.Total != tt.total {
				t.Errorf("total = %d, want %d", got.Total, tt.total)
			}
		})
	}
}

func TestReviewFiltersBySinceInclusiveOfTheBoundary(t *testing.T) {
	cs := newTestSession(t) // clock advances one second per record
	seed(t, cs, 4, "windowing")

	all := review(t, cs, nil)
	// Oldest of the four, which the clock placed at fixedStart.
	boundary := all.Observations[len(all.Observations)-1].RecordedAt

	got := review(t, cs, map[string]any{"since": boundary})
	if got.Total != 4 {
		t.Errorf("total = %d, want 4: since is inclusive of its boundary", got.Total)
	}
}

func TestReviewFiltersAcceptOffsetTimestamps(t *testing.T) {
	cs := newTestSession(t)
	seed(t, cs, 3, "zones")

	// fixedStart is 09:26:53Z. The same instant expressed at -07:00 must select
	// the same window; comparing the raw string against stored Z values would
	// not. This pins normalisation, not merely parsing.
	utc := review(t, cs, map[string]any{"since": "2026-03-14T09:26:53Z"})
	offset := review(t, cs, map[string]any{"since": "2026-03-14T02:26:53-07:00"})

	if utc.Total != offset.Total {
		t.Errorf("offset timestamp selected %d observations, UTC equivalent selected %d",
			offset.Total, utc.Total)
	}
	if utc.Total != 3 {
		t.Errorf("total = %d, want 3", utc.Total)
	}
}

func TestReviewRejectsMalformedTimestamps(t *testing.T) {
	cs := newTestSession(t)

	for _, field := range []string{"since", "before"} {
		t.Run(field, func(t *testing.T) {
			res := callTool(t, cs, "review_observations", map[string]any{field: "last tuesday"})
			if !res.IsError {
				t.Fatalf("a malformed %s was accepted", field)
			}
		})
	}
}

func TestReviewRejectsACursorIdWithoutItsTimestamp(t *testing.T) {
	cs := newTestSession(t)

	res := callTool(t, cs, "review_observations", map[string]any{"before_id": "some-id"})
	if !res.IsError {
		t.Fatal("before_id was accepted without before; the pair is the cursor")
	}
}

func TestReviewReportsTruncationWhenLimitCaps(t *testing.T) {
	cs := newTestSession(t)
	seed(t, cs, 5, "capping")

	got := review(t, cs, map[string]any{"limit": 2})
	if len(got.Observations) != 2 {
		t.Errorf("returned %d observations, want 2", len(got.Observations))
	}
	if !got.Truncated {
		t.Error("truncated = false, but the match is larger than the page")
	}
	if got.Total != 5 {
		t.Errorf("total = %d, want 5: total spans the whole match, not the page", got.Total)
	}
}

// TestReviewCapsObservationTextAndSaysSo covers the per-entry cap, which bounds
// the payload rather than the row count — limit does not, and 500 free-text
// observations reach the model as a result the SDK serialises twice.
func TestReviewCapsObservationTextAndSaysSo(t *testing.T) {
	cs := newTestSession(t)

	long := strings.Repeat("x", observationTextCap+500)
	res := callTool(t, cs, "record_observation", map[string]any{
		"observation": long,
		"class":       "verbose",
		"scope_ref":   "owner/repo",
	})
	if res.IsError {
		t.Fatalf("record: %s", errorText(t, res))
	}

	got := review(t, cs, map[string]any{})
	if len(got.Observations) != 1 {
		t.Fatalf("returned %d observations, want 1", len(got.Observations))
	}
	entry := got.Observations[0]
	if len(entry.Observation) > observationTextCap {
		t.Errorf("observation is %d bytes, want at most %d", len(entry.Observation), observationTextCap)
	}
	if !entry.ObservationTruncated {
		t.Error("observation_truncated = false on a capped entry; " +
			"a caller cannot otherwise tell a clipped observation from a short one")
	}
}

// TestReviewCapDoesNotSplitARune guards the boundary case the byte cap invites.
//
// Cutting a multi-byte rune in half produces invalid UTF-8, so the caller
// receives a corrupted trailing character rather than a clean truncation — see
// clip's doc comment for what encoding/json does with it, and why that rests on
// a v1 compatibility option rather than on the package. Observations are free
// text an agent composes, so non-ASCII in one is ordinary, not exotic.
func TestReviewCapDoesNotSplitARune(t *testing.T) {
	cs := newTestSession(t)

	// Three-byte runes do not divide into the cap evenly, so the cap lands
	// mid-rune and a byte-wise cut would split one.
	long := strings.Repeat("☃", observationTextCap)
	res := callTool(t, cs, "record_observation", map[string]any{
		"observation": long,
		"class":       "unicode",
		"scope_ref":   "owner/repo",
	})
	if res.IsError {
		t.Fatalf("record: %s", errorText(t, res))
	}

	got := review(t, cs, map[string]any{})
	if len(got.Observations) != 1 {
		t.Fatalf("returned %d observations, want 1", len(got.Observations))
	}
	text := got.Observations[0].Observation
	if !utf8.ValidString(text) {
		t.Errorf("capped observation is not valid UTF-8: %q", text[max(0, len(text)-8):])
	}
	if strings.ContainsRune(text, utf8.RuneError) {
		t.Error("capped observation carries U+FFFD; the cap split a rune")
	}
}

func TestReviewClassCountsSpanTheWholeMatch(t *testing.T) {
	cs := newTestSession(t)
	seed(t, cs, 4, "alpha")
	seed(t, cs, 2, "beta")

	// A page far smaller than the match: the tally must still describe all six.
	got := review(t, cs, map[string]any{"limit": 1})

	counts := map[string]int{}
	for _, cc := range got.ClassCounts {
		counts[cc.Class] = cc.Count
	}
	if counts["alpha"] != 4 {
		t.Errorf("alpha count = %d, want 4", counts["alpha"])
	}
	if counts["beta"] != 2 {
		t.Errorf("beta count = %d, want 2", counts["beta"])
	}
}

func TestReviewRejectsNonPositiveLimit(t *testing.T) {
	cs := newTestSession(t)

	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			res := callTool(t, cs, "review_observations", map[string]any{"limit": limit})
			if !res.IsError {
				t.Fatalf("limit %d was accepted; the floor is 1", limit)
			}
		})
	}
}

// TestReviewLimitCeiling pins the published ceiling to maxLimit.
//
// Both cases are written against the symbol, so retuning maxLimit moves both
// arms and they stay green — what the pair catches is the schema wiring
// drifting away from the constant, not the constant changing. Each arm bounds
// one side: the accept case fails if the wiring lands below maxLimit, the
// over-cap case if it lands above, so neither alone pins the ceiling. Nothing
// else would catch the drift — the published schema is the only enforcer,
// since Store.List substitutes a default for a non-positive limit and applies
// no upper clamp.
func TestReviewLimitCeiling(t *testing.T) {
	cs := newTestSession(t)

	for _, tc := range []struct {
		limit   int
		wantErr bool
	}{
		{limit: maxLimit, wantErr: false},
		{limit: maxLimit + 1, wantErr: true},
	} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			res := callTool(t, cs, "review_observations", map[string]any{"limit": tc.limit})
			if res.IsError != tc.wantErr {
				t.Fatalf("limit %d: IsError = %v, want %v", tc.limit, res.IsError, tc.wantErr)
			}
		})
	}
}

func TestReviewOnEmptyStoreSerializesAsArrays(t *testing.T) {
	cs := newTestSession(t)

	res := callTool(t, cs, "review_observations", nil)
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}

	// Asserted on the wire form, not the decoded value: a nil slice marshals to
	// null, which breaks a client that iterates the field.
	var wire struct {
		Observations *[]json.RawMessage `json:"observations"`
		ClassCounts  *[]json.RawMessage `json:"class_counts"`
	}
	if uerr := json.Unmarshal(raw, &wire); uerr != nil {
		t.Fatalf("decode: %v", uerr)
	}
	if wire.Observations == nil {
		t.Error("observations serialised as null, want []")
	}
	if wire.ClassCounts == nil {
		t.Error("class_counts serialised as null, want []")
	}
}

func TestReviewToleratesNullArguments(t *testing.T) {
	cs := newTestSession(t)
	seed(t, cs, 2, "nulls")

	// The limit default is what arms the panic this middleware prevents.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "review_observations",
		Arguments: json.RawMessage("null"),
	})
	if err != nil {
		t.Fatalf("null arguments tore down the call: %v", err)
	}
	if res.IsError {
		t.Fatalf("null arguments were refused: %s", errorText(t, res))
	}

	var out reviewResult
	decodeStructured(t, res, &out)
	if out.Total != 2 {
		t.Errorf("total = %d, want 2: null arguments should behave as no arguments", out.Total)
	}
}

// TestReviewPagesBackwardAcrossATimestampTie is the spec that pins the cursor
// design. It deliberately seeds observations that all share one recorded_at.
//
// A timestamp-only cursor passes a paging test built on distinct timestamps
// while silently dropping every remaining row in a boundary instant. Since
// concurrent sessions routinely record inside the same millisecond and the store
// is append-only, those rows would be permanently unreachable with no truncation
// signal. Holding the clock still is what makes this spec able to fail.
func TestReviewPagesBackwardAcrossATimestampTie(t *testing.T) {
	frozen := time.Date(2026, 5, 2, 11, 0, 0, 0, time.UTC)
	cs := sessionWithClock(t, func() time.Time { return frozen })

	const total = 7
	const pageSize = 2
	seed(t, cs, total, "tied")

	seen := map[string]bool{}
	args := map[string]any{"limit": pageSize}

	for page := 0; ; page++ {
		if page > total {
			t.Fatal("paging did not terminate; the cursor is not advancing")
		}
		got := review(t, cs, args)
		if len(got.Observations) == 0 {
			break
		}
		for _, obs := range got.Observations {
			if seen[obs.ID] {
				t.Fatalf("observation %s returned on more than one page", obs.ID)
			}
			seen[obs.ID] = true
		}
		last := got.Observations[len(got.Observations)-1]
		args = map[string]any{
			"limit":     pageSize,
			"before":    last.RecordedAt,
			"before_id": last.ID,
		}
	}

	if len(seen) != total {
		t.Errorf("paged over %d observations, want %d: rows sharing a timestamp "+
			"were skipped at a page boundary", len(seen), total)
	}
}

func TestReviewWindowsBackwardPastTheLimit(t *testing.T) {
	cs := newTestSession(t) // distinct, advancing timestamps
	const total = 12
	const pageSize = 5
	seed(t, cs, total, "walking")

	seen := map[string]bool{}
	args := map[string]any{"limit": pageSize}

	for page := 0; ; page++ {
		if page > total {
			t.Fatal("paging did not terminate")
		}
		got := review(t, cs, args)
		if len(got.Observations) == 0 {
			break
		}
		for _, obs := range got.Observations {
			seen[obs.ID] = true
		}
		last := got.Observations[len(got.Observations)-1]
		args = map[string]any{
			"limit":     pageSize,
			"before":    last.RecordedAt,
			"before_id": last.ID,
		}
	}

	if len(seen) != total {
		t.Errorf("reached %d observations, want %d", len(seen), total)
	}
}
