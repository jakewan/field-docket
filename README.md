# field-docket

[![CI](https://github.com/jakewan/field-docket/actions/workflows/ci.yml/badge.svg)](https://github.com/jakewan/field-docket/actions/workflows/ci.yml)

A per-machine store of classed observations, exposed over the [Model Context Protocol](https://modelcontextprotocol.io).

An agent session notices things — a rough edge, a surprising behavior, a place where guidance misfired. Those noticings die with the session. Anything later built on them rests on someone's recollection, which is a poor foundation for a decision.

field-docket gives a session somewhere to put them. An agent records a short observation with a caller-defined class; later, a separate pass reads them back and reasons over the accumulation. Whether something has happened once or five times becomes a fact rather than an impression.

**The server does not know what a class means.** Classes, scopes, and subjects are opaque strings, stored and returned without interpretation. field-docket holds evidence; it does not interpret it.

> **Status:** early. The recording and review surface is stable enough to use; adjudication — recording what was *decided* about a body of observations — is not built yet.

## Recording is separated from adjudication

This is the design's load-bearing constraint, and it is enforced structurally rather than by convention.

Recording is append-only and never gated. No stored state can cause a write to be refused, skipped, or silently dropped — a `record_observation` call fails only if its input is malformed, or if the docket itself cannot be served at all (see below). The observation table carries no column referring to any judgment about it, and `BEFORE UPDATE` / `BEFORE DELETE` triggers reject row mutation at the SQL layer.

What the invariant forbids is a *judgment about the record* deciding whether a write happens. An *environment precondition* — no disk, an unreadable database, a docket whose files something else has touched — is a different thing: the docket is unavailable, and saying so is not deciding what is worth recording.

The reason is that a store which can decline to record is a store that quietly decides what matters. Evidence collection and evidence evaluation are different acts, and mixing them produces a record shaped by the conclusions it was later used to support.

When adjudication arrives it will reference observations, never the reverse.

## MCP tools

| Tool | Purpose |
| --- | --- |
| `record_observation` | Append one observation. Returns its id and recorded timestamp. |
| `review_observations` | Read observations back, filtered and paged, with per-class counts over the whole match. |

### `record_observation`

| Field | Required | Notes |
| --- | --- | --- |
| `observation` | yes | The observation, in one sentence. |
| `class` | yes | Caller-defined category. Trimmed and lowercased; never rejected for its value. |
| `scope_kind` | no | `project` (default) or `user`. |
| `scope_ref` | no | Which project the observation arose in, as `owner/repo`. Required when `scope_kind` is `project`. |
| `subject` | no | Optional opaque pointer to what the observation is about — a path, URL, or identifier. |

The recording client is attributed automatically from the MCP handshake; there is no field for it.

`scope_kind` defaults to `project` deliberately. The opposite default fails silently: an observation about a project filed as user-level is invisible to a project-scoped review, and the store is append-only, so it cannot be corrected. Defaulting to `project` turns that into a loud error when `scope_ref` is missing — an error you can retry beats a misfile you cannot fix.

### `review_observations`

All filters are optional and combine with AND: `class`, `scope_kind`, `scope_ref`, `since`, `before` + `before_id`, `limit`.

Results come back newest first. `total` reports the size of the whole match and `truncated` says whether the page cut it short — the store never truncates silently. `class_counts` spans the entire match rather than the returned page, so it stays meaningful when results are capped.

To page backward, pass the last row's `recorded_at` **and** its `id` as `before` / `before_id`. Both are required together: timestamps are not unique when several sessions record concurrently, so a timestamp-only cursor would skip the rest of that instant.

## Observations are stored in the clear

Observation text is free-form and written by an agent from session context, which makes it the most likely place for something sensitive to end up by accident. Two things follow.

**Write observations in your own words.** Describe what went wrong; do not paste credentials, tokens, or raw error payloads.

**Redaction is deliberate and out-of-band.** The append-only triggers block deletion for every in-band caller, so removing an observation means dropping the triggers from a `sqlite3` session, deleting the row, and recreating them. That friction is intentional — it makes redaction a considered act rather than an available one — but it means a leaked secret has a known remedy.

The store is created mode `0600` inside a `0700` directory, and it is not encrypted at rest.

**A docket whose files are reachable by anyone else is not served.** At startup field-docket checks the database and, when they exist, its `-wal`, `-shm`, and `-journal` sidecars; if any of them carries a group or other permission bit, both tools refuse and say why. The reason is not only that someone could read the docket. field-docket creates a docket `0600`, so any other mode means something outside it has touched the files — and a mode carries no history of what it was before, so one that is `0644` now may have been `0666` earlier. The append-only triggers bind callers coming through this server, not anything else holding the file, so a docket in that state may no longer be the record it appears to be.

The most common benign cause is a docket that arrived some other way: restored from a backup, extracted from an archive under the usual `umask 022`, or made by hand in a `sqlite3` session. Modes are set when the docket is created and never repaired afterward, so such a docket keeps whatever permissions it came with, sidecars included — and that is what this check catches.

What that check does and does not cover:

- It runs **once, at startup**. Permissions changed while a session is already running are not noticed.
- It reads **mode bits only**, not ownership. A `0600` file owned by someone else passes.
- It does **not** inspect the store's directory. You may put the store wherever you like, including a directory you did not create for it; a `0600` file in a `0755` directory is not readable by others.
- It does not run on **Windows**, which has no POSIX mode bits — Go reports a synthetic mode there, so the check would refuse every store while telling you nothing. On Windows, file permissions are not a boundary this tool can describe.

`field-docket snapshot` still works on a refused docket, deliberately: it writes a `0600` copy you can examine before deciding whether to trust the original. Note that taking a snapshot *opens* the docket — it produces a consistent logical copy, not an untouched image. If you want the latter, copy the database and any `-wal`/`-shm`/`-journal` files beside it before running anything.

To go on using a docket as it is, list its path under `allow_unsafe_permissions` (see Configuration). The exemption is per-path and lasts until you remove it.

## Storage

SQLite in WAL mode, at `$XDG_STATE_HOME/field-docket/field-docket.db` (falling back to `~/.local/state`).

WAL is what allows several agent sessions on one machine to read and write the same store concurrently. It relies on shared memory, so the store must live on a local filesystem — placing it on NFS, SMB, or a sync-service folder will corrupt it.

The store grows without bound. There is no pruning yet.

## Configuration

Optional. With no config file at all, field-docket starts and uses its defaults.

To override, create `$XDG_CONFIG_HOME/field-docket/config.yml`:

```yaml
# Where the SQLite store lives. Defaults to
# $XDG_STATE_HOME/field-docket/field-docket.db
store: ~/.local/state/field-docket/field-docket.db

# Dockets to serve even though their files are reachable by more than their
# owner. Each entry is a store path. Entries are compared as paths, so a
# relative and an absolute spelling of the same file match, but listing one
# docket never exempts another. Omit this key to have every docket checked.
allow_unsafe_permissions:
  - /srv/shared/field-docket.db
```

Resolution order: the `--config` flag, then `$FIELD_DOCKET_CONFIG`, then the XDG path above.

## Installing

```bash
git clone https://github.com/jakewan/field-docket
cd field-docket
just install
```

That builds to `~/.local/bin/field-docket`. Register it with your MCP client as a stdio server running the `field-docket` command.

## Backups

`field-docket snapshot <path>` writes a consistent single-file copy of the store, suitable for backup.

Use it rather than copying the database file directly: a file copy of a live WAL database can capture a mid-transaction state that will not open.

## Project

- [Contributing](CONTRIBUTING.md) — setup, scope, testing approach, and PR posture.
- [Security policy](SECURITY.md) — how to report a vulnerability, and what this project claims about data handling and the supply chain.
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Changelog](CHANGELOG.md)

## License

[MIT](LICENSE)
