# PR and Commit Conventions

How pull requests, commits, and the changelog work in this project.

## PR Descriptions

(extension point: `pr-description-format`)

Structure a PR body as:

- **Overview** — the purpose: what the change accomplishes and why. Lead with this.
- **How it works** (optional) — only for non-obvious mechanics a reviewer cannot infer from the diff.
- **Issue references** — closing keywords (`Closes #N`); repeat the keyword for each issue (`Closes #1, closes #2`).

Avoid:

- Enumerating the diff file-by-file — the diff already shows what changed.
- Narrating the drafting journey ("earlier this did X, then I changed it").
- Scaffolding headers with no content under them.
- Hard-wrapping prose at a fixed column. Write one long line per paragraph and let the renderer wrap.

## Commit Messages

(extension point: `squash-commit-format`)

Conventional Commits: `type(scope): subject`.

- **Types**: `feat`, `fix`, `refactor`, `docs`, `build`, `ci`, `test`, `chore`.
- **Scopes**: a code change scopes to the `internal/` package it centers on — the directory name (`store`, `config`, … — illustrative, not a closed list, so a new package carries a scope without editing this rule), with one alias where the scope name diverges from the directory: `mcp` for the server and tool contract (`internal/server`). A change spanning several packages scopes to its center of gravity. Cross-cutting work that isn't a single package uses `ci`, `build`, `docs`, `rules`, `dx` (developer-experience: tooling, justfile, hooks), or `deps` (dependency updates, e.g. Dependabot bumps). A changelog-only commit — notably the post-PR-creation commit that anchors an entry to its PR number — belongs to no code area; scope it `docs` with no scope (`docs: ...`).
- **Body**: short prose stating *why* — the motivation, constraint, or problem solved — sized to the change, not its diff. A small change may need a one-line body or none. Don't restate the diff, don't narrate the journey ("the review surfaced...", "earlier this did X"), and don't re-derive rationale a durable doc (a design decision in `CLAUDE.md`, a doc comment) already records — point to it or omit it.
- **Issue references**: `Closes #N` (or `Related to #N`); repeat the keyword per issue.

## Changelog

(extension point: `changelog-convention`)

This project keeps a changelog in `CHANGELOG.md` following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html). It is written by hand, entry by entry, as part of the PR making the change — nothing generates it from commit history. At release prep a `## [X.Y.Z]` heading is inserted directly below `## [Unreleased]` so the accumulated entries fall under it, which is how the release workflow locates the notes for a tag; `CONTRIBUTING.md` carries the full release ritual.

A PR requires a changelog entry when it makes a **user-facing change**:

- New, changed, or removed MCP tools.
- Observable behavior changes (what is refused, what is returned, output shape).
- **Any change to the stored schema, or to how a stored value is normalized.** These are load-bearing beyond the usual bar: the store is append-only, so a normalization change partitions the record permanently — observations written before and after can never be reconciled — and a reader reasoning over accumulated evidence needs to know where the seam is.
- Bug fixes that affect user-visible results.
- A dependency update that resolves a known advisory. `govulncheck` runs on every PR and weekly, so an advisory it stops reporting across a bump is the signal — a reader scanning a changelog for security-relevant releases has nowhere else to look.
- Config-format changes.

No entry is needed for: internal refactors with no observable effect, test-only changes, CI/build/tooling, documentation, or agent rules.

Add the entry under `## [Unreleased]` in the matching category — `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, or `Security`. Keep it concise, user-facing, and present-tense.

Anchor each entry to its introducing PR as a trailing `(#N)`. Because the number isn't known until the PR exists, the entry can be authored with the tracking-issue number and corrected to the PR number once it's created.

When a PR corrects or refines the behavior of a feature still under `[Unreleased]`, amend that feature's existing entry in place rather than adding a separate `Fixed`/`Changed` line — you don't log a fix for behavior that never shipped.

`CONTRIBUTING.md` § Changelog entries states these same obligations for a human contributor, who has no reason to read this file. The duplication is deliberate, so **the two move together** — a change to what requires an entry, to the categories, or to the anchor convention lands in both or in neither.

## Branch Freshness

(extension point: `freshness-response-policy`)

When assessing how far a branch trails its base:

- **Up to date** — proceed.
- **Modestly behind** — note it; refreshing is optional.
- **Significantly behind, or conflicts are likely** — merge the base branch in first (this project merges, never rebases) before merging the PR.

## Code-Audit Fix vs. Defer

(extension point: `code-audit-practice`)

When acting on review findings:

- **Fix in place** when the finding is small, localized, and within the PR's stated purpose.
- **Defer to a GitHub issue** when addressing it would expand the PR's scope or is tangential to its purpose.
