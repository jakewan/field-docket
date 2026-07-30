package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jakewan/field-docket/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordDescription is the tool contract as the calling agent reads it. The
// class guidance lives here rather than in a rule because it is needed at call
// time, once the agent has already decided to record — that is the moment the
// full schema, descriptions included, is in front of it.
const recordDescription = `Append one observation to the local docket.

Recording is append-only and is never refused on the basis of what the docket ` +
	`already contains; only malformed input is rejected. Observations cannot be ` +
	`edited or deleted once written.

Keep an observation to one sentence describing what was observed. Reuse an ` +
	`existing class where one fits rather than coining a near-synonym — classes ` +
	`are how observations are grouped later, and a fragmented vocabulary makes ` +
	`the docket harder to read. Call review_observations to see which classes ` +
	`are already in use.

Write the observation in your own words. Do not paste credentials, tokens, or ` +
	`raw error payloads: the docket is stored unencrypted and entries cannot be ` +
	`removed in-band.`

// recordInput is one observation to append.
//
// Observation and Class lack omitempty and so are required; the schema rejects
// an omitted one before the handler runs. jsonschema tags carry plain-prose
// descriptions only.
type recordInput struct {
	Observation string `json:"observation"          jsonschema:"the observation, in one sentence"`
	Class       string `json:"class"                jsonschema:"caller-defined category this observation belongs to; stored verbatim and never interpreted by the server"`
	ScopeKind   string `json:"scope_kind,omitempty" jsonschema:"whether the observation belongs to a project or to the user; defaults to project"`
	ScopeRef    string `json:"scope_ref,omitempty"  jsonschema:"the project the observation arose in, as owner/repo; required when scope_kind is project"`
	Subject     string `json:"subject,omitempty"    jsonschema:"optional pointer to what the observation is about — a path, URL, or other identifier, stored and returned verbatim"`
}

// recordOutput confirms exactly what was persisted. recorded_at is echoed
// because the server owns the clock and the caller cannot know the stamp.
type recordOutput struct {
	ID         string `json:"id"`
	RecordedAt string `json:"recorded_at"`
}

// recordSchema publishes the scope_kind vocabulary and its default in the tool
// contract, so a client sees the real behaviour rather than it being buried in
// Go. The SDK injects the default when the caller omits the field, but only for
// non-required properties — which is why ScopeKind carries omitempty.
func recordSchema() *jsonschema.Schema {
	s := mustSchema[recordInput]("record_observation")
	s.Properties["scope_kind"].Enum = scopeKinds
	s.Properties["scope_kind"].Default = jsonString(scopeProject)
	return s
}

// recordObservation appends one observation.
//
// The refusal set here is the invariant made concrete: blank observation, blank
// class, and a project-scoped record with no scope_ref. Nothing else can cause a
// refusal, and in particular nothing read from the store can. See
// TestRecordRefusesOnlyMalformedInput.
func (d *deps) recordObservation(ctx context.Context, req *mcp.CallToolRequest, in recordInput) (*mcp.CallToolResult, recordOutput, error) {
	// Trim before validating so a whitespace-only value is rejected like a
	// missing one, and so the trimmed form is what gets stored.
	observation := strings.TrimSpace(in.Observation)
	if observation == "" {
		return toolError("observation is required and cannot be blank"), recordOutput{}, nil
	}

	// Class and scope_ref are identifiers used for grouping, so case and
	// surrounding whitespace are noise. Normalising them is not gating: no value
	// is rejected, but "Correctness" and "correctness " become the same bucket
	// rather than three. The store is append-only, so a fragmented vocabulary
	// cannot be cleaned up after the fact.
	class := strings.ToLower(strings.TrimSpace(in.Class))
	if class == "" {
		return toolError("class is required and cannot be blank"), recordOutput{}, nil
	}

	scopeKind := strings.TrimSpace(in.ScopeKind)
	if scopeKind == "" {
		// Defensive: the schema publishes the default, so this is reached only
		// if a client bypasses default injection.
		scopeKind = scopeProject
	}

	scopeRef := strings.ToLower(strings.TrimSpace(in.ScopeRef))
	if scopeKind == scopeProject && scopeRef == "" {
		// A loud, retryable error, chosen over the alternative failure mode:
		// silently filing a project observation as user-level would hide it from
		// every project-scoped review, and an append-only store offers no way to
		// correct it later.
		return toolError("scope_ref is required when scope_kind is project; pass the project as owner/repo, or set scope_kind to user"),
			recordOutput{}, nil
	}

	rec, err := d.store.Append(ctx, store.Observation{
		Class:     class,
		Text:      observation,
		ScopeKind: scopeKind,
		ScopeRef:  scopeRef,
		Subject:   strings.TrimSpace(in.Subject),
		Agent:     clientName(req),
	})
	if err != nil {
		return nil, recordOutput{}, err
	}

	return nil, recordOutput{ID: rec.ID, RecordedAt: rec.RecordedAt}, nil
}

// toolError builds a tool-level error result carrying msg.
//
// Returned rather than surfaced as a Go error so a malformed call reports a
// readable message to the model without failing the RPC.
func toolError(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
