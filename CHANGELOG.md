# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Record observations through the `record_observation` MCP tool: a free-text `observation` and a caller-defined `class`, with an optional scope (`scope_kind` of `project` or `user`, plus `scope_ref` naming the project as `owner/repo`) and an opaque `subject`. It returns the assigned id and the server-assigned timestamp, and attributes the recording client from the MCP handshake rather than from an input field. `class` and `scope_ref` are trimmed and lowercased so near-identical values land in one bucket; no value is rejected for its content. `scope_kind` defaults to `project`, which makes a missing `scope_ref` a loud, retryable error rather than silently filing a project observation where no project-scoped review will find it. (#1)
- Read observations back through the `review_observations` MCP tool, most-recent-first, under optional ANDed filters: `class`, `scope_kind`, `scope_ref`, a `since` lower bound, and a `(before, before_id)` keyset cursor for paging backward through a store larger than one page. It returns the page alongside `total` and per-class counts that span the whole filtered match rather than the page, so vocabulary drift is visible; all three come from one read transaction, so they describe a single consistent snapshot even while another session is recording. Row and per-observation text caps are reported through explicit truncation flags rather than applied silently. (#1)
- Store observations append-only. `BEFORE UPDATE` and `BEFORE DELETE` triggers reject row mutation at the SQL layer, and no stored state can cause a write to be refused — `record_observation` fails only on malformed input. Recording is never gated by any judgment about what is already recorded. (#1)
- Support several simultaneous sessions writing to one store. The database is opened in WAL mode with separate read and write handles, so a review never blocks a record, and concurrent first-opens converge on one schema rather than racing. (#1)
- Write a consistent single-file copy of the store with `field-docket snapshot <path>`, for backup. Copying the live database with a file-copying tool cannot be crash-consistent — the WAL sidecars are mid-transaction — so a snapshot is what makes a backup restorable. (#1)
- Load optional configuration from `$XDG_CONFIG_HOME/field-docket/config.yml`, overridable with a `--config` flag or the `FIELD_DOCKET_CONFIG` environment variable (precedence: flag, then environment, then the XDG default). Every key is optional and an absent default config is not an error, so the server starts with no configuration at all. A config named explicitly by flag or environment variable must exist and must parse: falling back to defaults on a mistyped path would leave none of the intended configuration in effect and say nothing about it. (#1)
- Report a build-stamped version to connecting MCP clients, derived from the release tag at build time (an untagged build reports `dev`). (#1)
- Exit cleanly (status 0) when the connected client disconnects, treating the normal end of a session as success rather than as an error. (#1)
- Refuse an unrecognized command-line argument instead of starting the server. A mistyped `snapshot` does not match the subcommand, so accepting the leftover argument would run a server that does nothing the operator asked for and report no problem. (#1)

### Changed

- Speak MCP protocol version `2026-07-28` when a client negotiates it; clients on earlier versions are unaffected. The recording client's name reaches the server differently under it — through per-request metadata rather than the `initialize` handshake — and that metadata makes the name optional where the handshake required it. An observation recorded by a client that omits it therefore carries an empty client name, which is a conforming client rather than a broken one. Because the store is append-only, this is a permanent seam: whether an empty name means "not supplied" or "not required" depends on which side of it the observation falls. (#2)
