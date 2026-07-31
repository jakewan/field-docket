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
  - ".github/workflows/release.yml"
  - "justfile"
---

# Toolchain / CI Parity

Local development and CI must run the same tool versions and the same formatters. When they drift, a change passes locally and fails in CI (or the reverse), or CI silently tests a different Go than developers run. These invariants are coupled across the files this rule conditions on — editing one file without its partner reintroduces the drift.

## golangci-lint version is one atomic value

The `golangci-lint` version is pinned in two places: `mise.toml` (the local binary) and the `golangci-lint-action` `version:` in `.github/workflows/ci.yml` (the CI binary). They must be identical. A config written for one minor can warn or error on another, so a mismatch means lint passes locally and fails in CI, or vice versa. When bumping, change both in the same commit.

## go.mod `go` directive tracks the mise Go pin

CI installs Go via `actions/setup-go` with `go-version-file: go.mod`, so the `go` directive in `go.mod` — not `mise.toml` — chooses the CI toolchain. Pin it to the same patch version as the `go` pin in `mise.toml`:

- A minor-only floor (`go 1.26`) lets CI float to the latest 1.26.x, drifting ahead of the pinned dev toolchain.
- A `.0` floor (`go 1.26.0`) pins CI to the oldest 1.26 patch, drifting behind it.
- The exact patch makes CI install the same Go developers run. This is also what `go mod init`/`go mod tidy` write by default.

Bump `go.mod` and `mise.toml` together on a Go upgrade. The coupling carries a second consequence: `govulncheck` reports standard-library advisories against the Go on `PATH`, so whichever pin a given run resolves decides what the vulnerability scan considers vulnerable. CI's scan job uses `setup-go` from `go.mod`, a developer's `just vuln` uses mise's — they agree only while the two pins do.

## The serverVersion ldflags symbol is injected from two places

`internal/server.serverVersion` is injected by the `justfile`'s `-ldflags -X` (local and `just install` builds) and by `.goreleaser.yaml`'s `ldflags` (release builds). Both name the symbol as a fully-qualified string, so **a rename or a package move must be made in both** — otherwise one path silently reverts to the `"dev"` default.

Nothing type-checks that string. The Go linker ignores an `-X` naming a symbol that does not exist, and a unit test asserting the handshake reports the variable's value passes whatever the `-X` says. `goreleaser check` is a config syntax linter and does not resolve it either. The only real guard is the `release-config` job's snapshot **build** in `.github/workflows/ci.yml`, which resolves the symbol for real — which is why that job's paths filter includes `**/*.go` and `go.mod`, not just the release-config files.

## mise.lock moves with every mise.toml pin

`mise.toml` sets `lockfile = true`, and `mise.lock` records each tool's resolved URL and checksum per platform. It is committed, and the two files must move together: run `mise lock` and commit the result in the same commit as any `mise.toml` version change.

**Nothing in CI enforces this.** No workflow job installs through mise — the only `jdx/mise-action` call, in `vuln.yml`'s toolchain-report job, sets `install: false`, and CI takes Go from `go.mod` and its other tools from action inputs. So a stale `mise.lock` is caught by review or not at all, which is why it is written here as a rule rather than left to a gate. (The donor repository this convention came from *did* have a CI consumer: a documentation job that installed through mise. This repository has no documentation book and dropped that job.)

Locally, a stale lock fails silently in an unhelpful direction: `mise install` updates an existing lockfile in place, so a bump followed by an install quietly rewrites `mise.lock` and hands you a diff you did not ask for. Review that diff rather than assuming your install could not have touched it. (`mise lock` is what *creates* the lockfile; `mise install` only maintains one that exists.)

A platform absent from the lock gets written in by whoever first installs on it, so an install from a new platform also produces a diff to review.

## The govulncheck tool dependency reaches the built binary

`govulncheck` is a `go.mod` tool dependency, so its requirements participate in the main module's version resolution. That is not confined to tooling: adding it raised `golang.org/x/sys` (via `golang.org/x/telemetry`), and `golang.org/x/sys` is linked into the binary through the SQLite driver. Every future update of `x/vuln` or its dependencies can do the same.

The consequence for review: a bump that looks like tooling can change the shipped binary's build list. Check `go list -deps ./cmd/field-docket` when a govulncheck update moves a shared dependency, and do not describe such a change as CI-only without checking.

## pre-commit formats with CI's formatter set; pre-push runs CI's test form

CI's golangci-lint enforces the formatters configured in `.golangci.yml` (`gofmt` + `goimports`). The pre-commit hook in `lefthook.yml` runs `golangci-lint fmt`, which applies that same configured set and re-stages the result — so an import-ordering issue can't pass the commit hook only to fail the lint job later. If the formatters enabled in `.golangci.yml` change, the hook inherits them automatically.

The pre-push test hook runs `just test-race`, matching CI's `go test -race`. Keep them the same form: this project's concurrency specs assert a load-bearing invariant, and without the detector they pass while the race they exist to catch goes unreported — so a hook running a plain `go test` would be green on exactly the change that most needs catching.
