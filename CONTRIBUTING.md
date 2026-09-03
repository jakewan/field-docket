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
mise install        # Install Go, golangci-lint, just, lefthook, goreleaser
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
just fmt                # Apply the formatters CI enforces, across the tree
just vuln               # Scan dependencies and stdlib for known vulnerabilities
just tidy-check         # Fail if go.mod/go.sum are not tidy
just release-check      # Validate release config and dry-run a snapshot build
just toolchain-outdated # Report mise-managed tools with newer versions
just install            # Install the binary to ~/.local/bin
```

`just install` uses `cp` to a temporary name followed by `mv`, which matters more here than it looks: an MCP client keeps a server process alive for a whole session, so overwriting a running binary is the normal case rather than an edge one.

### Dependencies and the toolchain

Go modules are watched by Dependabot and scanned by `govulncheck` in CI (on every change, and weekly). The mise-managed toolchain has no update bot — no ecosystem covers `mise.toml` — so it is reviewed by hand. The **Toolchain currency** workflow — kept apart from **Vulnerability scan** so a red result names which of the two fired — fails weekly when a pin is behind upstream, and that failure is the prompt; `just toolchain-outdated` runs the same query locally, against whatever mise is on your `PATH`. It gates nothing — that workflow is not a required check.

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

### Releases

Releases are cut by hand from a pushed tag. There is no release bot and no automated version bump.

**Choosing the version.** While the project is 0.x, a new feature bumps the minor, and a breaking change *also* bumps the minor rather than reaching 1.0.0. Past 1.0, a breaking change bumps the major. Worth revisiting as the project approaches 1.0.

**Cutting the release:**

1. `git fetch --tags` — the cross-check below needs the previous tag present locally.
2. Cross-check for a forgotten entry: `git log --oneline <last-tag>..HEAD`, and add anything missing under `## [Unreleased]`. For the first release there is no previous tag, so read `git log --oneline` over the whole history instead — the range form with an empty left side returns nothing and exits 0, which reads as "nothing was forgotten" when it means "nothing was examined". Conventional commit subjects are what make this scannable by type.
3. Insert a `## [X.Y.Z] - YYYY-MM-DD` heading into `CHANGELOG.md` directly below `## [Unreleased]`, so the accumulated entries fall under the new version heading. The release workflow locates the notes by matching that heading against the tag with its leading `v` stripped, so the heading carries no `v` — tag `v0.1.0` pairs with `## [0.1.0] - YYYY-MM-DD`. Leave `## [Unreleased]` in place, empty: the next contributor's entry goes under it.
4. Commit as `chore(release): vX.Y.Z`, tag, and push. The pushed `v*` tag is the release gate — it builds, signs, attests, and publishes.

Cross-checking before inserting the heading is what keeps a recovered entry in the release it describes. Insert first and `## [Unreleased]` is already the *next* release's section, so anything filed there per the convention below silently ships one release late.

## Pull Requests

- Keep PRs small and focused — each PR should serve a single purpose.
- PRs are squash-merged, so commit history within a branch doesn't need to be pristine.
- This project merges, never rebases.

Two things are easier to know up front than to discover afterward. They fail in opposite ways — one loudly, one silently:

- **Commits must be signed**, and this one is enforced. The default-branch ruleset requires signed commits with no bypass, so an unsigned commit cannot land however good the change is. GitHub's [commit signature verification](https://docs.github.com/en/authentication/managing-commit-signature-verification) guide covers setup. You find out by being refused.
- **The squash title should be a [Conventional Commit](https://www.conventionalcommits.org/en/v1.0.0/)** — `type(scope): subject`, using `feat`, `fix`, `refactor`, `docs`, `build`, `ci`, `test`, or `chore`. Nothing checks this, which is exactly why it is worth stating: cutting a release involves scanning `git log --oneline` for changes that should have a changelog entry, and a title parsing as none of these turns that scan from a filter by type into a read of every line. You find out at release time, when the scan is harder than it should be.

### Changelog entries

A PR that makes a **user-facing change** needs an entry in `CHANGELOG.md`. User-facing means:

- New, changed, or removed MCP tools.
- Observable behavior changes — what is refused, what is returned, output shape.
- **Any change to the stored schema, or to how a stored value is normalized.** These are load-bearing beyond the usual bar: the store is append-only, so a normalization change partitions the record permanently — observations written before and after can never be reconciled — and a reader reasoning over accumulated evidence needs to know where the seam is.
- Bug fixes that affect user-visible results.
- A dependency update that resolves a known advisory — a module in the `require` list; the entry goes under `Security`. `govulncheck` runs on every PR and on `main`, so an advisory reported on `main` but no longer reported on the bump's PR is the signal. A bump of the **pinned Go toolchain** is not this case: it is build tooling, and `SECURITY.md` treats the scan going red and a maintainer bumping the pin as the expected cycle rather than a notable event. It owes no entry even when it clears advisories — unless a particular advisory was severe and genuinely reachable, in which case explain why in the entry.
- Config-format changes.

No entry is needed for internal refactors with no observable effect, test-only changes, CI/build/tooling, documentation, or agent rules.

Add the entry under `## [Unreleased]` in the matching category — `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, or `Security` — and keep it concise, user-facing, and present-tense. Anchor it to its introducing PR as a trailing `(#N)`. Because that number isn't known until the PR exists, author the entry with the tracking-issue number and correct it once the PR is open.

When a PR corrects or refines the behavior of a feature still under `## [Unreleased]`, amend that feature's existing entry in place rather than adding a separate `Fixed` or `Changed` line — you don't log a fix for behavior that never shipped.

The same obligations are stated for tooling in `.claude/rules/pr-conventions.md` and `.github/copilot-instructions.md`, so a change to what requires an entry, to the categories, or to the anchor convention lands in all three or in none.
