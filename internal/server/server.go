// Package server builds the field-docket MCP server: it owns the tool contract
// and the store-backed handlers behind it.
//
// The split of responsibility is deliberate — this server is pure mechanism. It
// records observations and reads them back. It does not interpret a class, a
// scope, or a subject; those are caller-defined strings, stored and returned
// without interpretation. (Surrounding whitespace and — for the grouping keys,
// class and scope_ref — case are normalised on write, which is not
// interpretation: no value is ever rejected for its content.) Deciding what a
// body of observations means is the calling agent's job, and will become a
// separate tool operating on a separate entity.
//
// The invariant that shapes this package: recording is separated from
// adjudication. record_observation refuses malformed input and nothing else. No
// stored state can cause a write to be declined — see
// TestRecordRefusesOnlyMalformedInput, which asserts the refusal set exactly so
// that adding a state-derived refusal fails the build rather than passing
// quietly.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jakewan/field-docket/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName is the programmatic MCP identifier — lowercase, matching the
// binary, the Go module, and the mcpServers config key. serverTitle is the
// human-readable display name a client shows when it has one.
const (
	serverName  = "field-docket"
	serverTitle = "Field Docket"
)

// serverVersion is the release identity reported to MCP clients in the
// initialize handshake. It is overridden at build time via
// -ldflags "-X .../internal/server.serverVersion=<version>" (see the justfile,
// which derives the value from git describe); the "dev" default marks a build
// produced without that injection (plain go build, go test, go install).
//
// No unit test can guard the injection: asserting that the handshake reports
// this variable's value holds whatever the -X path says, and the linker ignores
// an -X naming a symbol that does not exist. The guard is the release-config
// snapshot build in CI, which resolves the symbol for real.
var serverVersion = "dev"

// scopeKinds is the recommended vocabulary published in the tool schema.
//
// It is a hint, not a storage constraint: the column accepts any string. The
// schema rejects an unrecognised value at the MCP boundary so a typo is caught
// loudly, while leaving the stored vocabulary open — constraining it in SQL
// would hard-code one caller's taxonomy into a general-purpose store.
var scopeKinds = []any{scopeProject, scopeUser}

const (
	scopeProject = "project"
	scopeUser    = "user"
)

// deps carries what the handlers need.
type deps struct {
	store *store.Store
}

// New builds the field-docket MCP server with both tools registered.
func New(st *store.Store) *mcp.Server {
	d := &deps{store: st}

	s := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Title:   serverTitle,
		Version: serverVersion,
	}, nil)
	s.AddReceivingMiddleware(tolerateNullArguments)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "record_observation",
		Description: recordDescription,
		InputSchema: recordSchema(),
	}, d.recordObservation)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "review_observations",
		Description: reviewDescription,
		InputSchema: reviewSchema(),
	}, d.reviewObservations)

	return s
}

// tolerateNullArguments rewrites a tools/call whose arguments are the literal
// JSON null to having no arguments at all, so a default-carrying input schema
// behaves the same as an omitted arguments field.
//
// Without it the SDK unmarshals a null payload into a nil arguments map and then
// panics applying a schema default into it, tearing down the session over
// non-conforming-but-harmless input. The SDK allocates the map and then
// overwrites it: it guards on len(data) > 0, and the literal "null" is four
// bytes, so it passes that guard and unmarshalling nils the map (go-sdk@v1.6.1
// mcp/tool.go, applySchema). jsonschema-go then writes into it via reflect
// SetMapIndex (jsonschema-go@v0.4.3 jsonschema/validate.go:741).
//
// Note the absent and literal-null cases differ: with arguments omitted the map
// stays non-nil and defaults apply cleanly, which is why a test that only omits
// a defaulted field exercises the safe path and would not catch a regression
// here. If a future SDK fixed the nil-map path, this middleware would become a
// no-op rather than a hazard.
//
// It fires whenever a non-required property carries a default, which is true of
// both tools here — review's limit and record's scope_kind — so the middleware
// is registered server-wide rather than reasoned about per tool.
func tolerateNullArguments(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
			if bytes.Equal(bytes.TrimSpace(call.Params.Arguments), []byte("null")) {
				call.Params.Arguments = nil
			}
		}
		return next(ctx, method, req)
	}
}

// clientName reports the recording client's name from the initialize handshake,
// or "" when the peer did not supply one.
//
// Derived rather than accepted as input: the caller cannot get it wrong or omit
// it, and it is one fewer field in the tool contract. Both hops are guarded
// because both are nilable — the SDK guards InitializeParams the same way in its
// own code — and an unguarded chain would panic on a client that skipped or
// malformed the handshake, which is the failure class tolerateNullArguments
// exists to prevent.
func clientName(req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return ""
	}
	params := req.Session.InitializeParams()
	if params == nil || params.ClientInfo == nil {
		return ""
	}
	return params.ClientInfo.Name
}

// mustSchema builds an input schema for T, panicking on failure.
//
// A schema-build failure is a programmer error in the struct definition, not
// runtime input, so it panics at construction rather than degrading a tool to
// missing defaults or bounds at call time.
func mustSchema[T any](tool string) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("server: building %s input schema: %v", tool, err))
	}
	return s
}

// jsonString renders s as a JSON string literal for use as a schema default.
func jsonString(s string) json.RawMessage {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("server: encoding schema default %q: %v", s, err))
	}
	return raw
}
