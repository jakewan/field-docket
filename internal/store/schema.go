package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// schemaVersion is the version the migration ladder brings a store up to. It is
// recorded in SQLite's user_version header field.
const schemaVersion = 1

// migration is one step of the ladder. Statements run in order, inside the
// single transaction that also bumps user_version, so a partially-applied
// migration cannot be observed.
type migration struct {
	version int
	stmts   []string
}

// migrations is the ordered ladder. Steps are appended, never edited: an
// already-migrated store will not re-run a step whose version it has passed.
//
// The DDL lives here as Go literals rather than embedded .sql files so the
// schema is greppable from the specs that assert against it.
var migrations = []migration{
	{
		version: 1,
		stmts: []string{
			// No column here refers to adjudication, and none ever may. The
			// dependency direction is the invariant: a future adjudication
			// entity references observations; observations never reference it.
			// A column such as adjudicated_at or resolved_at would give a
			// writer something to consult, and "recording is never gated"
			// would then erode by drift rather than by decision.
			//
			// STRICT rejects type confusion at write time instead of returning
			// a surprise at read time.
			//
			// scope_kind deliberately carries no CHECK constraint. The
			// recommended vocabulary (project, user) is published in the tool
			// schema, but constraining it here would hard-code one caller's
			// vocabulary into a general-purpose store, and widening it later
			// would mean SQLite's twelve-step table rebuild in a store whose
			// triggers exist to make rewriting rows impossible.
			`CREATE TABLE observation (
				id          TEXT NOT NULL PRIMARY KEY,
				recorded_at TEXT NOT NULL,
				class       TEXT NOT NULL,
				observation TEXT NOT NULL,
				scope_kind  TEXT NOT NULL,
				scope_ref   TEXT NOT NULL DEFAULT '',
				subject     TEXT NOT NULL DEFAULT '',
				agent       TEXT NOT NULL DEFAULT ''
			) STRICT`,

			// (recorded_at DESC, id DESC) is both the sort order and the keyset
			// cursor's key; the id column is part of it because recorded_at is
			// not unique when several sessions record concurrently.
			`CREATE INDEX observation_by_recorded ON observation(recorded_at DESC, id DESC)`,
			`CREATE INDEX observation_by_class ON observation(class, recorded_at DESC)`,
			`CREATE INDEX observation_by_scope ON observation(scope_kind, scope_ref, recorded_at DESC)`,

			// Append-only, enforced by the engine rather than by discipline.
			// This blocks mutation for every in-band caller; it is not
			// tamper-proofing, since DROP TRIGGER is ordinary SQL. That escape
			// is deliberate — it is the documented redaction path for an
			// observation that captured something sensitive — but it means the
			// guarantee is "no accidental mutation", not "no mutation".
			`CREATE TRIGGER observation_no_update BEFORE UPDATE ON observation
			 BEGIN SELECT RAISE(ABORT, 'observation is append-only'); END`,
			`CREATE TRIGGER observation_no_delete BEFORE DELETE ON observation
			 BEGIN SELECT RAISE(ABORT, 'observation is append-only'); END`,
		},
	},
}

// migrate brings db up to schemaVersion, applying only the steps it has not
// already passed.
//
// The whole ladder runs inside one BEGIN IMMEDIATE transaction, which is what
// makes concurrent first-runs safe: several agent sessions starting at once
// would otherwise each read user_version 0 and each try to create the table.
// An immediate transaction takes the write lock up front, so the losers block
// on the busy handler and then observe the winner's committed user_version.
func migrate(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning schema migration: %w", err)
	}
	defer func() {
		// A rollback after a successful commit reports ErrTxDone. That is the
		// normal path here, so it is tolerated explicitly rather than blanked —
		// errcheck runs with check-blank, and silencing it would also silence a
		// real rollback failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back schema migration: %w", rerr))
		}
	}()

	var current int
	if serr := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); serr != nil {
		return fmt.Errorf("reading schema version: %w", serr)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		for _, stmt := range m.stmts {
			if _, eerr := tx.ExecContext(ctx, stmt); eerr != nil {
				return fmt.Errorf("applying schema version %d: %w", m.version, eerr)
			}
		}
	}

	if current < schemaVersion {
		// PRAGMA does not accept bind parameters, so the version is
		// interpolated. It is an untainted package constant, never caller
		// input.
		setVersion := fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)
		if _, eerr := tx.ExecContext(ctx, setVersion); eerr != nil {
			return fmt.Errorf("recording schema version %d: %w", schemaVersion, eerr)
		}
	}

	if cerr := tx.Commit(); cerr != nil {
		return fmt.Errorf("committing schema migration: %w", cerr)
	}
	return nil
}
