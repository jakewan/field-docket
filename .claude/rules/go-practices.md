---
paths:
  - "**/*.go"
---

# Go Practices

Conventions for Go code in this repository.

## Errors

- Wrap errors with context as they propagate: `fmt.Errorf("appending observation: %w", err)`. Use `%w` so callers can `errors.Is`/`errors.As` the cause.
- Before relying on `errors.Is` to match a dependency's sentinel, confirm the cause is in the chain — `errors.Is` only traverses causes wrapped with `%w`, so one formatted with `%v` silently fails to match. When unsure, match a stable error code or type instead.
- On Go 1.26+, prefer `errors.AsType[T](err)` over the pre-declared-var-plus-`errors.As` form when recovering a typed error: `if e, ok := errors.AsType[T](err); ok { … }`. It matches on the concrete dynamic type, so a pointer representation in the chain will not satisfy a value target.
- Return errors; don't `log.Fatal` outside `main`. The single acceptable fatal is the top-level run error in `main`.
- Make validation errors specific and actionable — name what was wrong and what to do instead, so the message stands on its own to a model reading it as a tool result.
- Trim whitespace before checking a required string is non-empty (`strings.TrimSpace(s) == ""`), so a blank-looking value is rejected like a missing one.
- `errcheck` runs with `check-blank: true`, so discarding an error to `_` is itself a lint failure — `_ = f()` does not silence an unwanted error. Capture and inspect it, or fold a secondary cleanup error into the primary one with `errors.Join(...)`. In a deferred rollback, tolerate `sql.ErrTxDone` explicitly rather than blanking the result: a rollback after a successful commit reports it on the normal path, and blanking would also hide a real rollback failure.
- `govet`'s `shadow` check is enabled. Give inner error variables distinct names (`scanErr`, `writeErr`, `cerr`) rather than re-declaring `err`.

## Context

- Functions that do I/O or are cancellable take `context.Context` as the **first** parameter.
- Don't store a `context.Context` in a struct; pass it through the call chain.

## The store

This server's value is a durable record, so the store's guarantees are the product. Two invariants govern it.

- **Recording is separated from adjudication, and the separation is structural.** `record_observation` refuses malformed input, and otherwise refuses only when the store is unavailable — no stored state may cause a write to be declined, skipped, or silently dropped. An environment precondition (no disk, an unreadable database, files something outside this program has touched) is not a judgment about the record: it is established before any handler runs, decided in the composition root and passed to `server.New`, and refuses every tool alike. The test is whether the refusal consults the observations; one that needs to look at a row is not a precondition. The observation table carries no column referring to any judgment about a row, and the dependency direction is the invariant: a future adjudication entity references observations, never the reverse. A column such as `adjudicated_at` or `resolved_at` would give a writer something to consult, and the guarantee would then erode by drift rather than by decision. `TestObservationCarriesNoAdjudicationState` and `TestRecordRefusesOnlyMalformedInput` exist to make that erosion fail the build. The refusal test must keep asserting **accept**-cases, not only refusals: a refusal-only table stays green once a state-derived refusal is added, because the new refusal fires on inputs the table calls valid.
- **The store is per-machine and takes concurrent writes from several simultaneous sessions.** Three agent processes against one file is the normal case, not an edge. Anything touching the store is written for that, and pinned by a spec that exercises it — including one that re-execs the test binary, because in-process goroutines never exercise POSIX locking, WAL recovery, or `busy_timeout`. That distinction is not theoretical: the cold-start `SQLITE_BUSY` on `journal_mode=WAL` is invisible to an in-process test and fails outright across processes.

Mechanics that follow from those:

- **The read and write handles take different DSNs.** `_txlock=immediate` belongs on the write handle only. The driver consumes it in `newTx` gated on `!opts.ReadOnly`, so on a shared DSN any read transaction would issue `BEGIN IMMEDIATE`, take a write lock, and serialize reviews against records — negating WAL entirely.
- **Open read transactions with `&sql.TxOptions{ReadOnly: true}`**, and run a review's page, count, and aggregate queries inside **one** of them. Three pooled connections mean three WAL snapshots, so a concurrent write between them yields a total that disagrees with the page. Materialize the page and `rows.Close()` before issuing the aggregates — the transaction pins one connection, so iterating rows while querying on the same handle deadlocks.
- **Pragmas belong in the DSN, not in a post-`sql.Open` `Exec`.** The driver sorts `busy_timeout` to the front of the pragma list, so it is armed before `journal_mode` runs — which is what a cold start racing several processes needs.
- **Timestamps are stored as `t.UTC().Format("2006-01-02T15:04:05.000Z")`.** The trailing `Z` in Go's reference layout is a **literal character**, not a zone token (`Z07:00` is the token), so formatting a local time with this layout stamps local wall-clock time falsely labelled `Z` — wrong across machines and non-monotonic across a DST fall-back, permanently, in a store that cannot be rewritten. Fixed width is what makes lexicographic order equal chronological order, so `RFC3339Nano` is excluded: it strips trailing zeros and varies the width. A caller-supplied RFC 3339 filter must be parsed, converted with `.UTC()`, and re-formatted to this layout before binding — parsing is not normalization.
- **Handle both `rows.Close()` and `rows.Err()`.** Skipping the latter silently truncates a result set on a mid-iteration error, which under concurrency looks exactly like the data loss this store exists to prevent.
- **The sort key is `(recorded_at DESC, id DESC)` and the paging cursor is that whole pair.** `recorded_at` is not unique — concurrent writers share milliseconds — so a cursor on the timestamp alone drops every remaining row in the boundary millisecond, with no truncation signal and no way to recover it. Use SQLite's row-value comparison, which the index serves directly. A pagination spec that seeds distinct timestamps passes while the store loses rows, so the tie is the point of the test.

## MCP server

This server speaks JSON-RPC over stdio.

- **stdout is the protocol stream — write nothing else to it.** No `fmt.Println`/`fmt.Printf` to stdout in server code; diagnostics go to stderr via `log`.
- **The server stores; the caller interprets.** Classes, scopes, and subjects are caller-defined strings, stored and returned without interpretation. When you reach for a `const` that encodes what a class *means*, it belongs in the calling agent's judgment instead — or, if callers need the guidance at call time, in the tool description.
- Publish a tool's input constraints — defaults, bounds (`minimum`/`maximum`), required vs optional — in its JSON schema, not in handler code. The schema is the contract callers introspect, and the SDK enforces it before the handler runs. The installed `jsonschema-go` infers only a description from struct tags — not `default`/`minimum`/`maximum`, and it marks every non-`omitempty` field required — so a tool needing real constraints sets an explicit `*jsonschema.Schema` as `Tool.InputSchema`.
- **A literal-null arguments payload panics without the `tolerateNullArguments` middleware — do not remove it.** The absent and literal-null cases differ, and conflating them is the trap: with `arguments` *absent* the SDK leaves a freshly-allocated non-nil map and schema defaults apply cleanly, but the literal four-byte `null` passes the SDK's `len(data) > 0` guard and unmarshals the map to nil, after which `jsonschema-go` panics writing a default into it via `SetMapIndex` — tearing down the session over non-conforming-but-harmless input. It fires for any non-required property carrying a default, which is true of both tools here (`limit` and `scope_kind`), so the middleware is registered server-wide rather than reasoned about per tool. Cover both payload shapes in tests; a test that only omits the field exercises the safe path.
- Result-set limits must be surfaced in the structured output, never silently truncated — a caller cannot tell incomplete data from complete data otherwise. This includes per-field truncation: a capped free-text field ships a companion flag saying it was capped. `limit` bounds the row count but not the payload, and the SDK serializes a result twice (structured plus back-compat text), so budget accordingly.
- Build slices with `make([]T, 0, n)`. A nil slice marshals to `null`, which breaks an iterating client.
- Smoke-testing the running binary over stdio needs a driver that holds stdin open until each reply is read. Piping a batch of requests and letting stdin close races the EOF shutdown — the session tears down before responses flush, so the binary exits 0 with no output, a false pass. Prefer the in-memory transport for automated coverage; for a manual end-to-end check, drive the binary from a harness that reads each response before sending the next and closes stdin last.

## Tests

- Use the standard `testing` package — no external assertion or mocking frameworks.
- Prefer table-driven tests for behavior variations (valid input, invalid input, edge cases, error paths) — not just the happy path.
- Isolate filesystem state with `t.TempDir()`; register cleanup with `t.Cleanup`. `t.TempDir()` fails the test if it cannot remove the directory, so every store-opening test must close its handle in cleanup, and any test spawning child processes must wait for all of them before asserting.
- Tests describe **what** the code does from the caller's perspective, not **how**. An interface should exist because a test needs to substitute an implementation, not as speculative abstraction.
- Exercise tool behavior through an in-memory client/server session (`mcp.NewInMemoryTransports`), asserting on the structured result — and on `IsError` for the error paths.
- **Concurrency specs assert set equality, never order.** Concurrent writers complete in scheduler-chosen order, so a positional assertion is a race, not a contract.
- **Stress-run new or changed concurrency tests under `go test -race -count=N` before trusting them.** A single green run hides a scheduler-dependent flake. A test helper shared across concurrent goroutines needs its own synchronization — an injected clock is called from every writer, so it is part of the API contract that it be safe for concurrent use.
- A guard that has never been observed failing is not yet a guard. When adding one, break the thing it protects, watch it go red, and restore.

## Documentation

- Exported types, functions, and packages carry godoc comments beginning with the symbol's name (`// New builds …`). The `revive` `exported` lint rule enforces this — a missing or malformed comment fails CI.
- The repo-wide comment conventions — why-not-what, and comment durability — live in `.claude/rules/comment-conventions.md`.

## Modernization

This module tracks the forms `go fix` endorses. `go fix -diff ./...` reports them as a patch without applying anything, and `go fix ./...` applies them; `go tool fix help` lists the analyzers and what each one rewrites. The `errors.AsType[T]` preference under Errors is an instance of the same posture — its analyzer is `errorsastype`. Not every form preference in this file is: `make([]T, 0, n)` under MCP server is a wire-shape requirement no analyzer emits, and reading it as optional tidiness would be a defect.

**No gate runs it, deliberately.** `go fix -diff` exits non-zero on a non-empty diff and would make a one-line CI or hook check, but that would let the pinned Go version decide what the module is allowed to contain: a fixer added upstream would turn the build red with no code change, and the pin bump and the code rewrite would then have to land together. That is the coupling class `.claude/rules/toolchain-ci-parity.md` governs, and it is a heavier trade than the tidiness buys. Run it when a Go pin moves, or when you want to know.

The tool's reach is narrower than a form's applicability, so a clean `go fix -diff` means the analyzers matched nothing rather than that no site remains — `waitgroupgo`, for one, skips a closure that takes a parameter, which was every remaining `wg.Add(1)` site here until they were converted by hand. Prefer the modern form at a site the analyzer declines rather than reading its silence as approval.

A rewrite is not automatically behaviour-preserving just because a tool emitted it. `waitgroupgo` is the standing counter-example: `sync.WaitGroup.Go` carries the precondition `The function f must not panic` in its own doc comment, because it skips `Done` and re-panics where a hand-written `defer wg.Done()` runs. Substituting it into a function that can panic changes what `Wait` observes. Read what the analyzer substitutes before accepting it, and treat a changed concurrency spec as changed (see Tests).
