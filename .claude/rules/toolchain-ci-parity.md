---
paths:
  - "mise.toml"
  - "mise.lock"
  - "go.mod"
  - "lefthook.yml"
  - ".golangci.yml"
  - ".goreleaser.yaml"
  - ".github/workflows/ci.yml"
  - ".github/workflows/vuln.yml"
  - ".github/workflows/toolchain.yml"
  - ".github/workflows/release.yml"
  - "justfile"
---

# Toolchain / CI Parity

Local development and CI must run the same tool versions and the same formatters. When they drift, a change passes locally and fails in CI (or the reverse), or CI silently tests a different Go than developers run. These invariants are coupled across the files this rule conditions on — editing one file without its partner reintroduces the drift.

## A tool pinned in both mise.toml and a workflow is one atomic value

Some tools are pinned twice — in `mise.toml` (the local binary) and as an action's `version:` input (the CI binary). Each pair must be identical, and a bump moves both in the same commit:

- **golangci-lint** — `mise.toml` and the `golangci-lint-action` `version:` in `.github/workflows/ci.yml`. A config written for one minor can warn or error on another, so a mismatch means lint passes locally and fails in CI, or vice versa.
- **GoReleaser** — `mise.toml` and the `goreleaser-action` `version:` in both `.github/workflows/ci.yml` and `.github/workflows/release.yml`. This is the tool that builds and signs published artifacts, so a floating range means `just release-check` validates against a different binary than the one that ships. `SECURITY.md` also asserts that CI pins it from an explicit `version:` input, which a range form would falsify.

The two spellings differ and that is not drift: mise pins a bare patch, both workflow inputs carry the same patch with a leading `v`. Only one of the two actions actually requires it — `golangci-lint-action` validates its input against a `v`-anchored pattern and throws on a bare patch, while `goreleaser-action` accepts either and prepends the `v` itself (read from each action's source at the SHA pinned here; if either changed, the symptom would be a loud failure at install rather than a wrong binary). Matching the two keeps one convention rather than two.

**Nothing detects a mismatch between a `mise.toml` pin and its workflow input.** Dependabot bumps an action's `uses:` SHA, never its inputs, and the weekly `toolchain-report` job in `.github/workflows/toolchain.yml` reads `mise.toml` alone — so bumping the mise pin and forgetting the workflow leaves that report green the following week while the drift stands. The report prompts the bump; nothing prompts the other half of it. Review is the only guard, which is why this is written down rather than left to a gate.

## go.mod `go` directive tracks the mise Go pin

CI installs Go via `actions/setup-go` with `go-version-file: go.mod`, so the `go` directive in `go.mod` — not `mise.toml` — chooses the CI toolchain. Pin it to the same patch version as the `go` pin in `mise.toml`:

- A minor-only floor (`go 1.26`) lets CI float to the latest 1.26.x, drifting ahead of the pinned dev toolchain.
- A `.0` floor (`go 1.26.0`) pins CI to the oldest 1.26 patch, drifting behind it.
- The exact patch makes CI install the same Go developers run. This is also what `go mod init`/`go mod tidy` write by default.

Bump `go.mod` and `mise.toml` together on a Go upgrade. The coupling carries a second consequence: `govulncheck` reports standard-library advisories against the Go on `PATH`, so whichever pin a given run resolves decides what the vulnerability scan considers vulnerable. CI's scan job uses `setup-go` from `go.mod`, a developer's `just vuln` uses mise's — they agree only while the two pins do.

**The Go pin also has an upper bound, which the parity above does not express.** golangci-lint analyses with the `go/types` of the Go release it was itself built against — `golangci-lint --version` reports that release — and each golangci-lint release announces which Go minor it adds support for. A Go minor is published before the golangci-lint release supporting it, and the weekly toolchain report names the two tools independently, so taking the Go line on its own is the report's most natural misreading. Move the pair together, and take the supporting release from golangci-lint's own release notes rather than assuming the newest patch covers it.

The failure is loud rather than silent, which makes `just lint` the gate that catches it and the one to run when moving the pair: golangci-lint refuses to start and names both versions — `the Go language version (goX.Y) used to build golangci-lint is lower than the targeted Go version (X.Z)`. Every other gate passes, so a bump verified with anything less looks clean. Expect the pair to be briefly un-bumpable, too: mise's `minimum_release_age` default hides very recent releases, so the supporting golangci-lint may be unavailable for a few days after it ships, holding the Go pin behind upstream — and the weekly report will call that lag every week while it holds, with no way to say the lag is known.

## The serverVersion ldflags symbol is injected from two places

`internal/server.serverVersion` is injected by the `justfile`'s `-ldflags -X` (local and `just install` builds) and by `.goreleaser.yaml`'s `ldflags` (release builds). Both name the symbol as a fully-qualified string, so **a rename or a package move must be made in both** — otherwise one path silently reverts to the `"dev"` default.

Nothing type-checks that string. The Go linker ignores an `-X` naming a symbol that does not exist, and a unit test asserting the handshake reports the variable's value passes whatever the `-X` says. `goreleaser check` is a config syntax linter and does not resolve it either. The only real guard is the `release-config` job's snapshot **build** in `.github/workflows/ci.yml`, which resolves the symbol for real — which is why that job's paths filter includes `**/*.go` and `go.mod`, not just the release-config files.

## mise.lock moves with every mise.toml pin

`mise.toml` sets `lockfile = true`, and `mise.lock` records each tool's resolved URL and checksum per platform. It is committed, and the two files must move together: run `mise lock` and commit the result in the same commit as any `mise.toml` version change.

**Nothing in CI enforces this.** No workflow job installs through mise — the only `jdx/mise-action` call, in `toolchain.yml`'s toolchain-report job, sets `install: false`, and CI takes Go from `go.mod` and its other tools from action inputs. So a stale `mise.lock` is caught by review or not at all, which is why it is written here as a rule rather than left to a gate.

**The two commands are not interchangeable, and only one of them is safe after a pin change.** `mise lock` refreshes every platform already recorded in the lockfile. An install rewrites the changed tool's entry to the new version and **drops all of its platform blocks** — checksums and URLs for every platform, the local one included — leaving an entry nothing can be verified against. It does this whether or not it actually installs anything; an already-present version still triggers the rewrite while mise reports there was nothing to do. So a pin bump followed by an install alone silently strips that tool's verification data, and the paragraph above is why nothing downstream objects.

Run `mise lock` after any pin change, and read the resulting diff rather than assuming an earlier install could not have touched it.

Both behaviours were observed directly on mise 2026.7.7 by bumping a pin, running an install by itself, and reading `git diff mise.lock`; `mise lock --help` describes the multi-platform refresh, and states that where no lockfile exists it only *shows* what would be created. Mise is not pinned in `mise.toml`, so a contributor's own version governs, and `.github/workflows/toolchain.yml` pins a newer one for the weekly report — the probe is provenance for the one version it ran on, not a standing guarantee, so re-run it if it matters. The symptom of a change here is a lockfile entry carrying a version with no platform blocks under it.

With the pin unchanged, a platform absent from the lock instead gets written in by whoever first installs on it, so an install from a new platform also produces a diff to review.

## The govulncheck tool dependency reaches the built binary

`govulncheck` is a `go.mod` tool dependency, so its requirements participate in the main module's version resolution. That is not confined to tooling: adding it raised `golang.org/x/sys` (via `golang.org/x/telemetry`), and `golang.org/x/sys` is linked into the binary through the SQLite driver. Every future update of `x/vuln` or its dependencies can do the same.

The consequence for review: a bump that looks like tooling can change the shipped binary's build list. Check `go list -deps ./cmd/field-docket` when a govulncheck update moves a shared dependency, and do not describe such a change as CI-only without checking.

## pre-commit formats with CI's formatter set; pre-push runs CI's test form

CI's golangci-lint enforces the formatters configured in `.golangci.yml` (`gofmt` + `goimports`). The pre-commit hook in `lefthook.yml` runs `golangci-lint fmt`, which applies that same configured set and re-stages the result — so an import-ordering issue can't pass the commit hook only to fail the lint job later. If the formatters enabled in `.golangci.yml` change, the hook inherits them automatically.

The pre-push test hook runs `just test-race`, matching CI's `go test -race`. Keep them the same form: this project's concurrency specs assert a load-bearing invariant, and without the detector they pass while the race they exist to catch goes unreported — so a hook running a plain `go test` would be green on exactly the change that most needs catching.
