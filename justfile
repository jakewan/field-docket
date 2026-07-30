# Version stamped into the binary. No --always: on a tag-less repo git describe
# would otherwise succeed with a bare commit SHA and the `|| echo dev` fallback
# would never fire. Without it, describe fails cleanly when no tag exists and the
# fallback yields "dev". So: tag-less -> dev; clean tagged checkout -> v0.1.0;
# later commits -> v0.1.0-N-gSHA; dirty tree -> a -dirty suffix.
version := `git describe --tags --dirty 2>/dev/null || echo dev`

# The -ldflags below injects internal/server.serverVersion — the SAME symbol
# .goreleaser.yaml injects at release time. A rename of that symbol must be
# changed in BOTH places, or one path silently reverts to "dev". Neither a unit
# test nor `goreleaser check` catches that: the linker ignores an -X naming a
# symbol that does not exist. The CI release-config job's snapshot build is the
# guard, because it resolves the symbol for real.

# Build the binary
build:
    @mkdir -p bin
    go build -ldflags "-X github.com/jakewan/field-docket/internal/server.serverVersion={{version}}" -o bin/field-docket ./cmd/field-docket

# Install the binary to ~/.local/bin (atomic cp+mv — survives "text file busy")
install: build
    @mkdir -p ~/.local/bin
    # cp+mv, not cp alone: mv swaps the directory entry atomically, so the install
    # succeeds even while an older copy is running (cp alone fails with "text file
    # busy" on Linux when overwriting a running binary). MCP clients keep a server
    # process alive for a whole session, so that case is the norm here, not an edge.
    cp bin/field-docket ~/.local/bin/field-docket.tmp && mv ~/.local/bin/field-docket.tmp ~/.local/bin/field-docket
    @echo "Installed to ~/.local/bin/field-docket"
    @echo "(ensure ~/.local/bin is in your PATH)"

# Run all tests
test:
    go test ./...

# Run all tests under the race detector (what CI runs; the concurrency specs are
# only meaningful with it enabled)
test-race:
    go test -race ./...

# Run tests with coverage
test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out

# Lint with golangci-lint
lint:
    golangci-lint run ./...

# Format code
fmt:
    gofmt -w .

# Tidy module dependencies
tidy:
    go mod tidy

# Verify module dependencies
verify:
    go mod verify

# Fail if go.mod/go.sum are not tidy (reports the diff, does not rewrite)
tidy-check:
    go mod tidy -diff

# Scan dependencies and the standard library for known vulnerabilities
vuln:
    go tool govulncheck ./...

# Report mise-managed tools with newer versions available.
# --bump is required, not cosmetic: every pin in mise.toml is an exact version,
# and plain `mise outdated` compares against the latest release matching the
# requested spec — which for an exact pin is always the pin itself, so it
# reports "up to date" no matter how far upstream has moved. --local keeps a
# contributor's global mise config out of the report.
toolchain-outdated:
    mise outdated --bump --local

# Preview the unreleased changelog section, to review and polish before a release
changelog:
    git cliff --unreleased

# Validate release config and dry-run a snapshot build (mirrors the CI gate).
# --snapshot skips cosign signing and never runs provenance (CI-only, OIDC-bound),
# so this proves the build, not the hardening. --single-target keeps the local
# loop fast; it still resolves the ldflags symbol, which is what this guards. CI
# runs the same build across the full release matrix.
release-check:
    goreleaser check
    goreleaser build --snapshot --single-target --clean

# Clean build artifacts
clean:
    rm -rf bin/ dist/
    rm -f coverage.out

# Install git hooks via lefthook
hooks:
    lefthook install
