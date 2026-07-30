package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jakewan/field-docket/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reviewDescription is the tool contract as the calling agent reads it.
const reviewDescription = `Read recorded observations back, newest first.

All filters are optional and combine with AND. total reports the size of the ` +
	`whole match and truncated says whether the page cut it short, so an ` +
	`incomplete result is always distinguishable from a complete one. ` +
	`class_counts tallies the entire match rather than the returned page, which ` +
	`makes it usable for seeing which classes dominate and which vocabulary is ` +
	`already in use.

To page backward, pass the last returned observation's recorded_at as before ` +
	`AND its id as before_id. Both are needed: timestamps are not unique when ` +
	`several sessions record at once, so before on its own would skip the rest ` +
	`of that instant.`

// maxLimit bounds a single page. The SDK serialises a result twice (structured
// plus a back-compat text rendering), so the wire cost is about double the
// apparent payload; an uncapped review would blow the client's tool-result
// budget.
const maxLimit = 500

// observationTextCap bounds one observation's text in a review result. Rows are
// capped as well as counted: limit bounds how many observations come back, not
// how large they are, and a page of long entries can still overrun the caller's
// budget. A truncated entry is marked rather than silently shortened.
const observationTextCap = 1000

// reviewInput narrows a review. Every field is optional.
type reviewInput struct {
	Class     string `json:"class,omitempty"      jsonschema:"restrict to observations of this class"`
	ScopeKind string `json:"scope_kind,omitempty" jsonschema:"restrict to observations at this scope"`
	ScopeRef  string `json:"scope_ref,omitempty"  jsonschema:"restrict to observations that arose in this project, as owner/repo"`
	Since     string `json:"since,omitempty"      jsonschema:"restrict to observations recorded at or after this RFC 3339 timestamp"`
	Before    string `json:"before,omitempty"     jsonschema:"paging cursor: the recorded_at of the last observation from the previous page; pass with before_id"`
	BeforeID  string `json:"before_id,omitempty"  jsonschema:"paging cursor: the id of the last observation from the previous page; pass with before"`
	Limit     int    `json:"limit,omitempty"      jsonschema:"maximum observations to return, most recent first; defaults to 50"`
}

// observationView is one observation as returned. Optional fields are omitted
// when empty so the wire shape stays minimal.
type observationView struct {
	ID          string `json:"id"`
	RecordedAt  string `json:"recorded_at"`
	Class       string `json:"class"`
	Observation string `json:"observation"`
	ScopeKind   string `json:"scope_kind"`
	ScopeRef    string `json:"scope_ref,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Agent       string `json:"agent,omitempty"`

	// ObservationTruncated marks an entry whose text was shortened to fit the
	// per-entry cap. Without it a caller cannot tell a clipped observation from
	// one that was genuinely that short.
	ObservationTruncated bool `json:"observation_truncated,omitempty"`
}

type classCount struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

type reviewOutput struct {
	Observations []observationView `json:"observations"`
	ClassCounts  []classCount      `json:"class_counts"`

	// Total is the size of the whole match, not of this page.
	Total int `json:"total"`

	// Truncated reports that the match is larger than the returned page. A
	// result-set limit is always surfaced rather than silently applied: a caller
	// cannot otherwise distinguish incomplete data from complete data.
	Truncated bool `json:"truncated"`
}

// reviewSchema publishes the limit default and bounds, and the scope_kind
// vocabulary, in the tool contract.
func reviewSchema() *jsonschema.Schema {
	s := mustSchema[reviewInput]("review_observations")
	s.Properties["limit"].Default = json.RawMessage("50")
	// Zero has no "no cap" meaning here — an uncapped review over a store that
	// has accumulated for months would overrun the caller's budget — so the
	// floor is 1 rather than 0.
	s.Properties["limit"].Minimum = jsonschema.Ptr(1.0)
	s.Properties["limit"].Maximum = jsonschema.Ptr(float64(maxLimit))
	s.Properties["scope_kind"].Enum = scopeKinds
	return s
}

// reviewObservations returns a filtered page of observations with counts.
func (d *deps) reviewObservations(ctx context.Context, _ *mcp.CallToolRequest, in reviewInput) (*mcp.CallToolResult, reviewOutput, error) {
	filter := store.Filter{
		Class:     strings.ToLower(strings.TrimSpace(in.Class)),
		ScopeKind: strings.TrimSpace(in.ScopeKind),
		ScopeRef:  strings.ToLower(strings.TrimSpace(in.ScopeRef)),
		BeforeID:  strings.TrimSpace(in.BeforeID),
		Limit:     in.Limit,
	}

	// Timestamps are parsed and normalised to UTC here rather than passed
	// through. A caller may send any offset, and the stored representation is
	// UTC, so comparing a raw -07:00 string against stored Z values would select
	// the wrong window. Parsing alone is not normalisation.
	if raw := strings.TrimSpace(in.Since); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return toolError("since must be an RFC 3339 timestamp: %v", err), reviewOutput{}, nil
		}
		filter.Since = t.UTC()
	}
	if raw := strings.TrimSpace(in.Before); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return toolError("before must be an RFC 3339 timestamp: %v", err), reviewOutput{}, nil
		}
		filter.Before = t.UTC()
	}
	if filter.BeforeID != "" && filter.Before.IsZero() {
		return toolError("before_id must be passed together with before"), reviewOutput{}, nil
	}

	recs, total, classes, err := d.store.List(ctx, filter)
	if err != nil {
		return nil, reviewOutput{}, err
	}

	// Built with make so an empty result serialises as [] rather than null,
	// which a client iterating the field would otherwise fail on.
	views := make([]observationView, 0, len(recs))
	for _, r := range recs {
		text, clipped := clip(r.Text, observationTextCap)
		views = append(views, observationView{
			ID:                   r.ID,
			RecordedAt:           r.RecordedAt,
			Class:                r.Class,
			Observation:          text,
			ScopeKind:            r.ScopeKind,
			ScopeRef:             r.ScopeRef,
			Subject:              r.Subject,
			Agent:                r.Agent,
			ObservationTruncated: clipped,
		})
	}

	counts := make([]classCount, 0, len(classes))
	for _, c := range classes {
		counts = append(counts, classCount{Class: c.Class, Count: c.Count})
	}

	return nil, reviewOutput{
		Observations: views,
		ClassCounts:  counts,
		Total:        total,
		Truncated:    total > len(views),
	}, nil
}

// clip shortens s to at most n characters, reporting whether it did.
func clip(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	return s[:n], true
}
