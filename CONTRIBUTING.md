# Contributing to field-docket

## Issues

This project uses **problem-framed issues**. The issue template asks you to describe:

- **The problem** you're experiencing
- **Current behavior** — what happens today
- **Desired behavior** — what you'd expect instead
- **Why it matters** — the impact on your workflow

Focus on describing the problem clearly. Solution ideas are welcome as supplementary context, but the issue should stand on the strength of the problem description alone.

## Scope

field-docket is a single-purpose MCP server: it records classed observations to a per-machine store and reads them back. It holds evidence; it does not interpret it.

Two constraints shape what fits here, and both are worth reading before proposing a change:

- **Recording is separated from adjudication.** A proposal that lets stored state gate, suppress, or filter a write is out of scope by construction — not a trade-off to weigh. Recording what was *decided* about a body of observations is in scope, but as a separate entity that references observations, never as a column on one.
- **The server does not interpret a class.** Classes, scopes, and subjects are caller-defined strings. A proposal that adds a fixed taxonomy, a closed vocabulary, or logic keyed on a particular class value belongs in the calling agent instead.

Contributions should stay within this focused scope. If you're unsure whether something fits, open an issue describing the problem first.

## Development

### Setup

Tool versions are managed by [mise](https://mise.jdx.dev/). After cloning:

```bash
mise install        # Install Go, golangci-lint, just, lefthook, goreleaser, git-cliff
just hooks          # Install git hooks (lefthook)
```

Tool versions come from `mise.toml`, and their checksums from the committed `mise.lock` — `mise install` verifies downloads against it. If you change a version pin, run `mise lock` and commit the updated lockfile in the same change.

### Build, Test, Lint

All commands go through [just](https://github.com/casey/just):

```bash
just build              # Build binary to bin/
just test               # Run all tests
just test-race          # Run all tests under the race detector (what CI runs)
just lint               # Run golangci-lint
just vuln               # Scan dependencies and stdlib for known vulnerabilities
just tidy-check         # Fail if go.mod/go.sum are not tidy
just release-check      # Validate release config and dry-run a snapshot build
just toolchain-outdated # Report mise-managed tools with newer versions
just install            # Install the binary to ~/.local/bin
```

`just install` uses `cp` to a temporary name followed by `mv`, which matters more here than it looks: an MCP client keeps a server process alive for a whole session, so overwriting a running binary is the normal case rather than an edge one.

### Dependencies and the toolchain

Go modules are watched by Dependabot and scanned by `govulncheck` in CI (on every change, and weekly). The mise-managed toolchain has no update bot — no ecosystem covers `mise.toml` — so it is reviewed by hand. The weekly scheduled run fails when a pin is behind upstream, and that failure is the prompt; `just toolchain-outdated` runs the same check locally. It gates nothing — that workflow is not a required check.

Several pins move in pairs; `.claude/rules/toolchain-ci-parity.md` records which and why. `SECURITY.md` describes the full supply-chain posture, including what the scanning does and does not guarantee.

### Testing Approach

The project uses BDD-style/outside-in TDD:

- Write failing tests before production code.
- Drive the MCP tool surface from acceptance tests that exercise the server over an in-memory client/server session, then build inward.
- Tests use the standard `testing` package — no external test frameworks.
- Use table-driven tests for multiple scenarios; isolate filesystem state with `t.TempDir()`.

Two testing constraints are specific to this project and easy to get wrong:

- **Concurrency specs run under `-race`, and one of them re-execs the test binary.** In-process goroutines never exercise POSIX locking, WAL recovery, or `busy_timeout` — the cold-start `SQLITE_BUSY` this store handles is invisible to an in-process test and fails outright across processes. Assert set equality, never ordering: concurrent writers complete in scheduler-chosen order.
- **A guard is not a guard until it has been seen failing.** The specs pinning the two invariants (`TestRecordRefusesOnlyMalformedInput`, `TestObservationCarriesNoAdjudicationState`) are load-bearing precisely because they are easy to weaken into something that stays green. If you touch either, break the thing it protects, watch it go red, and restore.

### Project name

Write **field-docket** in lowercase throughout — it is the binary, the Go module, the `mcpServers` config key, and the MCP server's machine `name`. The display title (`Field Docket`, Title Case) appears only in the MCP handshake, mirroring the convention of a lowercase machine name paired with a human-readable title.

## Pull Requests

- Keep PRs small and focused — each PR should serve a single purpose.
- PRs are squash-merged, so commit history within a branch doesn't need to be pristine.
- This project merges, never rebases.
