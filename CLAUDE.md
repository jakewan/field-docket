# field-docket — Agent Guide

This file orients an AI agent (or a new contributor) working in this repository. It is self-contained: everything needed to work here is described below or in the linked in-repo files.

## What field-docket is

A general-purpose [MCP](https://modelcontextprotocol.io) server that records classed observations to a per-machine store and reads them back. An agent session notices things; those noticings die with the session, and anything later built on them rests on recall. This gives them somewhere to go.

Two invariants shape every design decision here. Read them before changing anything.

**1. Recording is separated from adjudication.** Recording is append-only and never gated: no stored state may cause a write to be refused, skipped, or silently dropped, and `record_observation` fails only on malformed input or when the store itself is unavailable. A store that can decline to record is a store that quietly decides what matters — so deciding what a body of observations *means* is a separate act, on a separate entity that references observations rather than a column on one.

A refusal the invariant forbids and a refusal it permits reach the caller identically, so the distinction has to be drawn deliberately rather than read off the symptom. The discriminator: **does the refusal consult the observations?** An environment precondition — no disk, an unreadable database — refuses every tool alike and decides nothing about what is worth recording. One that needs to look at a row is not a precondition.

**2. The store is per-machine and takes concurrent writes from several simultaneous sessions.** Three agent processes against one SQLite file is the normal case, not an edge. Everything touching the store is written for that.

`.claude/rules/go-practices.md § The store` carries the full treatment of both — how the separation is enforced structurally, the seam a precondition is decided at, the tests that make erosion fail the build, and the mechanics that follow from concurrency. It loads whenever a Go file is read.

## Status and layout

Early. The server exposes two tools. `record_observation` appends one observation — a free-text `observation` and a caller-defined `class`, plus an optional scope (`scope_kind` of `project` or `user`, with `scope_ref` naming the project as `owner/repo`) and an opaque `subject`. It returns the assigned id and the server-assigned timestamp. `review_observations` reads them back most-recent-first under optional ANDed filters (class, scope, a `since` lower bound, and a `(before, before_id)` keyset cursor for paging backward), returning the page alongside `total` and per-class counts spanning the whole filtered match, plus truncation flags. The binary also carries one non-MCP subcommand, `field-docket snapshot <path>`, which writes a consistent single-file copy for backup.

```
cmd/field-docket/     # binary entry point (composition root, stdio serve loop, snapshot subcommand)
internal/server/      # MCP server construction, the tool contract, and both handlers
internal/store/       # the SQLite store: schema, append, query, ids, path resolution
internal/config/      # optional config file loading, including the store-permission exemption list
```

Further packages arrive when a change needs them — do not create them speculatively.

## Build, test, lint

Tool versions are managed by [mise](https://mise.jdx.dev/) (`mise.toml`, with checksums in the committed `mise.lock` — regenerate it with `mise lock` whenever a pin changes); tasks run through [just](https://github.com/casey/just) (`justfile`). One-time setup:

```sh
mise install     # install pinned Go, golangci-lint, just, lefthook, goreleaser
just hooks       # install git hooks (lefthook)
```

Everyday commands:

```sh
just build              # build the binary to bin/ (with the version ldflags)
just test               # go test ./...
just test-race          # go test -race ./... — what CI and the pre-push hook run
just lint               # golangci-lint run ./...
just fmt                # golangci-lint fmt ./... — the formatter set CI enforces
just tidy-check         # fail if go.mod/go.sum are not tidy (CI runs the same check)
just vuln               # govulncheck over dependencies and the standard library
just release-check      # goreleaser check + snapshot build
just toolchain-outdated # report mise-managed pins behind upstream (what the weekly report runs)
just install            # build and install to ~/.local/bin
```

Formatting is enforced by golangci-lint's configured formatters (`gofmt`, `goimports`) — there is no separate format-check step. The `lefthook` hooks run formatting on commit and lint/test on push.

## Development approach

This project uses [BDD][bdd]-style/outside-in [TDD][tdd] for non-trivial code: write a failing behavior test from the caller's perspective first, let it drive the API, then implement the minimum to pass and refactor under the test's safety net. Tests use the standard `testing` package (no external frameworks), favor table-driven cases, exercise tool behavior through an in-memory MCP client/server session, and isolate filesystem state with `t.TempDir()`. Skip the ceremony for trivial work (typos, single-line fixes, documentation, these instruction files).

Go authoring conventions are in `.claude/rules/go-practices.md` (loaded when Claude reads a Go file). It carries the store's mechanics, which have several sharp edges that are not obvious from reading the code.

## Key design decisions

- **Single binary, daemonless.** It serves a session over stdio and exits. No background process, no network service, no outbound request.
- **MCP over stdio is JSON-RPC.** stdout carries the protocol and nothing else — see `.claude/rules/go-practices.md § MCP server`. Exiting on stdin EOF is normal shutdown.
- **The server stores; the caller interprets.** Classes, scopes, and subjects are opaque strings the server never branches on — see `.claude/rules/design-fork-adjudication.md` for why that constrains what may be encoded here.
- **A published enum is a hint, not a storage constraint.** `scope_kind` ships `project`/`user` as a schema `enum`, so a typo is rejected loudly at the MCP boundary while the column itself accepts any string — `.claude/rules/design-fork-adjudication.md` carries the reasoning and the cost of widening one later.
- **Normalization is not gating.** `class` and `scope_ref` are trimmed and lowercased on write so `Correctness` and `correctness ` land in one bucket rather than three; `observation` and `subject` are trimmed but not lowercased, since neither is a grouping key. No value is ever *rejected* for its content. This matters because the store is append-only: a fragmented vocabulary cannot be cleaned up afterward.
- **`scope_kind` defaults to `project`, and that direction is deliberate.** The opposite default fails silently — an observation about a repository filed as user-level is invisible to every project-scoped review and unfixable in an append-only store. Defaulting to `project` converts that into a loud, retryable error when `scope_ref` is blank.
- **SQLite via `modernc.org/sqlite`.** Pure Go, so `CGO_ENABLED=0` holds and the release matrix cross-compiles. The alternatives were disqualified by the concurrency invariant rather than merely beaten by it: a flat file means hand-rolling cross-process read-modify-write, and bbolt takes an exclusive lock for the lifetime of an open handle — with stdio servers living a whole session, the second agent would block forever.
- **Config is optional and its absence is not an error.** There is no required key; a missing file yields defaults and the server starts. `TestConfigDefaultsWhenAbsent` pins that.

## Conventions in this repo

- `.claude/rules/comment-conventions.md` — repo-wide comment conventions (why-not-what, comment durability); always loaded.
- `.claude/rules/go-practices.md` — Go authoring conventions, including the store's concurrency and timestamp mechanics (loaded when Claude reads a Go file).
- `.claude/rules/pr-conventions.md` — PR descriptions, commit format, changelog policy, branch freshness, fix-vs-defer.
- `.claude/rules/pr-waste-patterns.md` — what counts as reviewer-distracting waste in a diff.
- `.claude/rules/no-personal-details.md` — keep personal/identifying details out of this public repo.
- `.claude/rules/toolchain-ci-parity.md` — keeping the pinned local toolchain and CI in lockstep (loaded when Claude reads a pinned-toolchain file).
- `.claude/rules/github-actions-pinning.md` — pin every action to a commit SHA (loaded when Claude reads a workflow).
- `.claude/rules/design-fork-adjudication.md` — how value-laden design forks are settled here.
- `README.md` — what the tool is, its tool surface, storage properties, and install.
- `CONTRIBUTING.md` — contributor setup, scope, PR posture, the changelog-entry obligation, and the release ritual with its semver policy.
- `SECURITY.md` — reporting channel, plus the data-handling, supply-chain, and release claims. It asserts the store's permissions, durability, and redaction path, so a change to `internal/store/` should be checked against it; it also asserts what CI scans, pins, and verifies, so a change to the workflows, `mise.lock`, or the Dependabot config should be checked against it too.
- `CODE_OF_CONDUCT.md` — Contributor Covenant 2.1, with conduct reports routed the same way security reports are.
- `.github/copilot-instructions.md` — review guidance for GitHub Copilot.

[bdd]: https://en.wikipedia.org/wiki/Behavior-driven_development
[tdd]: https://en.wikipedia.org/wiki/Test-driven_development
