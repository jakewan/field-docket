# Security Policy

## Reporting a vulnerability

Please report security issues privately through GitHub's [private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability) on this repository's **Security** tab, rather than opening a public issue.

If that route is unavailable to you for any reason, open a regular issue saying only that you have a security report and how to reach you — no details — and you'll get a private channel back. Never put the details in a public issue.

Include what you did, what happened, and what you expected — a reproduction is the most useful thing you can send. Please allow time for a fix before disclosing publicly.

## Supported versions

field-docket is in early development and cuts no tagged releases yet, so the supported surface is the current `main` branch. Fixes land there; there is no backport channel.

## Data handling

field-docket is the unusual case among small MCP servers: it exists to **keep** data, so the storage properties are the security surface.

- **Observations are stored unencrypted, on the local filesystem.** The store is a SQLite database under `$XDG_STATE_HOME/field-docket/` (falling back to `~/.local/state/`). The directory is created `0700` and the database file `0600`, and the `-wal`/`-shm` sidecars inherit the database's mode at creation. Those modes are set at creation and are not repaired afterward, so a store that arrives another way — restored from a backup, or made by hand — keeps whatever permissions it came with, sidecars included. Filesystem permissions are the only confidentiality boundary — there is no encryption at rest and no passphrase.
- **The consequence: treat an observation as readable by anything running as your user.** The tool description instructs callers to write an observation in their own words and to keep credentials, tokens, and raw error payloads out of it. That is guidance to a calling model, not an enforced filter — the server cannot inspect the meaning of a free-text string.
- **Recorded observations cannot be deleted in-band.** `BEFORE UPDATE` and `BEFORE DELETE` triggers reject row mutation, and no tool exposes a delete. This is deliberate: a store that can quietly drop a record is not evidence. It also means an observation that captured something sensitive needs an out-of-band remedy — see Redaction below.
- **Nothing leaves the machine.** The server opens no network listener, makes no outbound request, and sends data nowhere except back to the caller that asked for it. It speaks MCP over stdio as a subprocess of the calling agent; stdout carries only the JSON-RPC protocol stream, and diagnostics go to stderr.
- **Backups are the operator's decision, and they move the boundary.** `field-docket snapshot <path>` writes a consistent single-file copy at mode `0600`. The mode is set explicitly rather than inherited: SQLite creates a `VACUUM INTO` destination at its own default (`0644`), unlike the `-wal`/`-shm` sidecars. But permissions travel no further than the machine — wherever that copy is sent carries the full corpus, including the `scope_ref` values, which are project identifiers and so are metadata even when the observation text is generic.

### Redaction

The append-only triggers block every in-band caller, not every process. `DROP TRIGGER` is ordinary SQL, so a local operator can remove them, delete the offending row, and recreate them. That escape is intentional — a leaked credential needs a known remedy — but it means the guarantee is "no accidental mutation", not "no mutation". Do not read the triggers as tamper-proofing.

### Durability

The store runs in WAL mode with `synchronous=NORMAL`. That is crash-safe against process death but **can lose the most recent committed transactions on a power loss or kernel panic**. The trade is deliberate — one `fsync` per observation buys little for this workload — but it is worth knowing in a store whose value is its record.

WAL also coordinates readers and writers through a shared-memory file, which requires every process using the database to be on the same host — SQLite documents that WAL does not work over a network filesystem. Do not place the store on NFS, SMB, or a sync-service folder, including via a symlink.

## Credential model

field-docket holds no credentials of its own. It authenticates to nothing, reads no token, and has no configuration slot for one. Its access is its own process's filesystem access.

## Build and CI supply chain

- **Dependencies and the standard library are scanned for known vulnerabilities.** [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) runs on every pull request, on pushes to `main`, and weekly on a schedule. It reports advisories whose vulnerable symbols are reachable from this code. The scan is **advisory, not blocking** — it is not a required status check, so it informs review rather than gating merges.
- **Build-time dependencies are checksum-verified.** Go modules are verified against the checksum database and `go.sum`, re-hashed in CI by `go mod verify`, and held tidy by `go mod tidy -diff` so entries no longer required cannot linger. `govulncheck` itself is a `go.mod` tool dependency, so the scanner is covered by the same verification as everything else.
- **CI pins its own toolchain; `mise.lock` covers local development.** Tool versions live in `mise.toml` with their resolved download URLs and checksums in the committed `mise.lock`, and `mise install` verifies against it — but no CI job installs through mise, so the lockfile governs a contributor's machine rather than the build. CI pins independently: Go comes from `go.mod` via `actions/setup-go`, golangci-lint and GoReleaser from their actions' explicit `version:` inputs. Read the lockfile as reproducibility for local work, not as a control on the published artifacts.
- **Actions are pinned by commit digest.** Every `uses:` in every workflow names a full commit SHA with the version as a trailing comment — a tag can be repointed, a commit SHA cannot.
- **Update automation covers Go modules and GitHub Actions.** Dependabot watches both weekly, and bumps the action SHAs along with their version comments.
- **The mise toolchain is reviewed manually.** No update bot covers `mise.toml`, so a separate weekly workflow checks the pins against upstream and **fails when any is behind**. The deliberate failure is what surfaces the report at all, since `mise outdated` exits 0 whether or not a pin has moved; how loudly it surfaces depends on a setting this repository does not control, because GitHub notifies on every completed run by default and "Only notify for failed workflows" is an opt-in account option. That report is kept in its own workflow rather than alongside the scan above, so a red result names which of the two fired — a failure notification carries the workflow name, not the job name. It blocks nothing (neither workflow is a required check). `just toolchain-outdated` runs the same check locally. The pinned version of mise itself lives in that workflow rather than in `mise.toml`, so nothing reports it; it moves only when a maintainer moves it.
- **Scheduled scanning stops silently in a quiet repository.** GitHub disables scheduled workflows after 60 days without repository activity. That is one repository-level clock rather than one per workflow, so the vulnerability scan and the toolchain report stop together — read a gap in either one's weekly runs as a symptom, not as a clean result. Re-enabling a disabled scheduled workflow also rebinds its notifications to whoever re-enabled it.

This describes a detector paired with a human response, not a project that is continuously free of known advisories. Go patch releases routinely fix reachable standard-library symbols, so the scan going red is expected periodically and is resolved by a maintainer bumping the pinned toolchain.

## Releases

Release artifacts are built by GoReleaser on a pushed `v*` tag. The checksums file is signed with [cosign](https://docs.sigstore.dev/) keyless signing — no managed keys; the certificate is bound to the release workflow's OIDC identity, so verification uses `--certificate-identity-regexp` and `--certificate-oidc-issuer`. SLSA build provenance is attested over the published archives and checksums.

The binary is built with `CGO_ENABLED=0`. The SQLite implementation is [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), a pure-Go translation rather than a cgo binding — so a release carries no C toolchain into the build and links no system SQLite. That choice is what makes the cross-compiled matrix buildable at all, and it also means SQLite advisories reach this project through a Go module version rather than through the host's system libraries.
