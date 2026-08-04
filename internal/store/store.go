// Package store persists observations in a local SQLite database.
//
// # Why SQLite
//
// The store takes concurrent writes from several agent sessions on one machine
// — three processes against one file is the normal case, not an edge case. That
// requirement disqualifies the simpler options rather than merely losing to
// them. A flat append-only log survives concurrent appends, but the adjudication
// step this store is built to accept next is a mutation, and mutating a flat
// file across processes means hand-rolling advisory locking and atomic rename:
// a worse SQLite. An embedded key-value store such as bbolt takes an exclusive
// lock for the lifetime of the open handle, and these servers hold their handle
// for a whole session, so the second session would block indefinitely.
//
// SQLite in WAL mode is the option where N long-lived processes each hold an
// open handle, readers never block writers, and exactly one writer proceeds at a
// time with a bounded retry.
//
// # Recording is separated from adjudication
//
// Append takes an observation and returns what was stamped. It accepts no state
// argument and performs no query, so "some stored state caused a write to be
// refused" is not expressible in its signature. The schema carries the other
// half: no column on the observation row refers to any judgment about it.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// sqliteBusy is SQLITE_BUSY. Declared here rather than imported from the
// driver's generated lib package, which is large and would be pulled in for one
// integer.
const sqliteBusy = 5

// Cold-start retry budget. See initialize.
const (
	initAttempts = 12
	initBackoff  = 25 * time.Millisecond
)

// isBusy reports whether err is SQLite's "database is locked".
func isBusy(err error) bool {
	serr, ok := errors.AsType[*sqlite.Error](err)
	return ok && serr.Code() == sqliteBusy
}

// timeLayout is the stored timestamp format: fixed width, always UTC.
//
// Fixed width is what makes lexicographic comparison equal chronological
// comparison, which is what lets the since/before filters be plain string
// comparisons that the indexes can serve. time.RFC3339Nano is unusable here
// because it strips trailing zeros, so its width varies and string ordering
// silently diverges from time ordering.
//
// The trailing Z is a literal character in Go's reference layout, not a zone
// token — Z07:00 is the token. Formatting a non-UTC time with this layout would
// stamp local wall-clock time wearing a UTC label, which is why every write goes
// through formatTime rather than calling Format directly.
const timeLayout = "2006-01-02T15:04:05.000Z"

// defaultLimit bounds a List that does not ask for a size.
const defaultLimit = 50

// formatTime renders t in the stored representation. The UTC conversion is the
// load-bearing half; see timeLayout.
func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// Observation is a caller-supplied observation, before the store stamps it.
//
// The observation text is Text rather than Observation: Record embeds this
// struct, and a field of the same name as its parent type resolves to the
// embedded struct instead of the string, which the compiler accepts and only
// fails at the database driver.
type Observation struct {
	Class     string
	Text      string
	ScopeKind string
	ScopeRef  string
	Subject   string
	Agent     string
}

// Record is a persisted observation, as read back.
type Record struct {
	ID         string
	RecordedAt string
	Observation
}

// Filter narrows a List. Every field is optional; set fields combine with AND.
type Filter struct {
	Class     string
	ScopeKind string
	ScopeRef  string

	// Since restricts to observations recorded at or after this instant.
	Since time.Time

	// Before and BeforeID are the backward-paging cursor and are used together:
	// the pair identifies the last row of the previous page. Before alone acts
	// as a coarse exclusive time bound. See List for why the pair is required
	// for correct paging.
	Before   time.Time
	BeforeID string

	// Limit caps the returned rows. Zero means defaultLimit.
	Limit int
}

// ClassCount is one row of the per-class tally over a filtered match.
type ClassCount struct {
	Class string
	Count int
}

// Store is a handle on the observation database.
type Store struct {
	readDB  *sql.DB
	writeDB *sql.DB
	now     func() time.Time
}

// Option configures a Store at open time.
type Option func(*Store)

// WithClock replaces the wall clock, so specs can make ordering deterministic.
//
// Tests need this rather than merely preferring it: two observations recorded in
// the same millisecond tie on recorded_at, and the id tiebreak within a
// millisecond is random, so "most recent first" would be undefined.
//
// now must be safe for concurrent use. Append calls it, and several agent
// sessions record against one store at once; the default, time.Now, is safe, but
// a counter-based test clock needs its own synchronisation.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// dsn builds a connection string for path.
//
// The pragmas are carried in the DSN rather than executed after sql.Open so the
// driver applies them to every pooled connection as it is created, and because
// the driver sorts busy_timeout to the front of the list — arming the busy
// handler before journal_mode runs (modernc.org/sqlite sqlite.go, "Push
// 'busy_timeout' first"). That ordering matters on a cold start, where
// several processes can race to flip a brand-new file into WAL and the loser
// needs to wait rather than fail. If the driver ever stopped sorting, a cold
// start under contention would fail outright rather than wait — loudly, not
// silently.
//
// writable selects the one asymmetric parameter, _txlock. See Open.
func dsn(path string, writable bool) string {
	params := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		// The standard WAL pairing. Crash-safe against process death; the
		// residual exposure is losing the most recently committed transactions
		// on a power cut, which is accepted because an fsync per observation
		// buys little for a local evidence log.
		"_pragma=synchronous(NORMAL)",
	}
	if writable {
		params = append(params, "_txlock=immediate")
	}
	return "file:" + path + "?" + strings.Join(params, "&")
}

// Open connects to the store at path, creating and migrating it if needed.
//
// Two handles, with two different DSNs. The split is not a micro-optimisation:
//
// Reads and writes are separated because issuing a second query while a *sql.Rows
// is still open would wait on a connection that the open Rows holds. On a single
// pooled handle that is a permanent deadlock, not a timeout, and List does
// exactly that when it tallies classes.
//
// _txlock=immediate is on the write handle only. The driver consumes it in
// exactly one place — the BeginTx path, gated on the transaction not being
// read-only (modernc.org/sqlite tx.go, newTx — the sole reader of beginMode) —
// so it governs the migration transaction and nothing else. An autocommit
// INSERT never reaches that path; it takes the write lock directly, so the
// deferred-lock-upgrade hazard the parameter exists to avoid does not arise on
// the record path at all. Putting it on the read handle would be
// actively harmful: any read transaction would then issue BEGIN IMMEDIATE, take
// a write lock, and serialise reviews against records — the opposite of what
// WAL is here to provide.
func Open(ctx context.Context, path string, opts ...Option) (s *Store, err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating store directory: %w", err)
	}

	// Create the file with restrictive permissions before the driver can.
	// SQLite stamps the -wal and -shm sidecars with the main file's mode when it
	// creates them, during the first WAL-mode open — earlier than any chmod
	// could run. Observations can carry project detail, so the mode has to be
	// right at creation rather than corrected afterwards.
	f, ferr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case ferr == nil:
		if cerr := f.Close(); cerr != nil {
			return nil, fmt.Errorf("closing newly created store file: %w", cerr)
		}
	case !errors.Is(ferr, os.ErrExist):
		return nil, fmt.Errorf("creating store file: %w", ferr)
	}

	st := &Store{now: time.Now}
	for _, opt := range opts {
		opt(st)
	}

	// From here on a failure must close whatever is already open, or the caller
	// gets an error and a leaked handle.
	defer func() {
		if err != nil {
			err = errors.Join(err, st.Close())
			s = nil
		}
	}()

	writeDB, werr := sql.Open("sqlite", dsn(path, true))
	if werr != nil {
		return nil, fmt.Errorf("opening store for writing: %w", werr)
	}
	st.writeDB = writeDB
	// One connection, so writes from this process serialise here rather than
	// contending in SQLite. That leaves the busy handler responsible only for
	// cross-process contention, which is what it is good at.
	writeDB.SetMaxOpenConns(1)

	readDB, rerr := sql.Open("sqlite", dsn(path, false))
	if rerr != nil {
		return nil, fmt.Errorf("opening store for reading: %w", rerr)
	}
	st.readDB = readDB

	if ierr := initialize(ctx, writeDB); ierr != nil {
		return nil, ierr
	}

	return st, nil
}

// initialize establishes the connection and brings the schema up to date,
// retrying while SQLite reports the database as locked.
//
// The retry exists for one specific race, and it is not one busy_timeout covers.
// Switching a database into WAL mode needs a brief exclusive lock, and SQLite
// returns SQLITE_BUSY for that transition immediately rather than invoking the
// busy handler — so when several sessions start against a store that does not
// exist yet, the losers fail outright instead of waiting. Every other contended
// operation is handled by busy_timeout.
//
// This only ever bites on a genuinely new store: WAL mode is recorded in the
// database header, so once any process has set it, later opens see it already
// set and the pragma becomes a lock-free no-op. The budget is therefore sized
// for a cold-start stampede, not for steady-state contention.
//
// The SQLITE_BUSY claim above is empirical rather than read off a specification,
// and TestSeparateProcessesRecordConcurrentlyWithoutLoss is the evidence:
// shrinking the budget to one attempt makes it fail with SQLITE_BUSY well inside
// the busy_timeout window. It fails only under repetition, though — a single run
// passes with the retry gone — so do not read one green run as showing this is
// dead code.
func initialize(ctx context.Context, writeDB *sql.DB) error {
	var err error
	for attempt := range initAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(ctx.Err(), err)
			case <-time.After(time.Duration(attempt) * initBackoff):
			}
		}

		if err = writeDB.PingContext(ctx); err != nil {
			if isBusy(err) {
				continue
			}
			return fmt.Errorf("connecting to store: %w", err)
		}
		if err = migrate(ctx, writeDB); err != nil {
			if isBusy(err) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("store stayed locked through %d attempts: %w", initAttempts, err)
}

// Close releases both handles, reporting any failure from either.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.readDB != nil {
		if err := s.readDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing store read handle: %w", err))
		}
		s.readDB = nil
	}
	if s.writeDB != nil {
		if err := s.writeDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing store write handle: %w", err))
		}
		s.writeDB = nil
	}
	return errors.Join(errs...)
}

// Append records o and returns it as stamped.
//
// This is the whole write path. It takes no state argument, reads nothing, and
// can fail only on a storage error — there is no branch on which stored data
// could cause an observation to be dropped.
func (s *Store) Append(ctx context.Context, o Observation) (Record, error) {
	at := s.now()
	id, err := newUUIDv7(at)
	if err != nil {
		return Record{}, err
	}
	rec := Record{ID: id, RecordedAt: formatTime(at), Observation: o}

	const insert = `INSERT INTO observation
		(id, recorded_at, class, observation, scope_kind, scope_ref, subject, agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	if _, ierr := s.writeDB.ExecContext(ctx, insert,
		rec.ID, rec.RecordedAt, o.Class, o.Text,
		o.ScopeKind, o.ScopeRef, o.Subject, o.Agent,
	); ierr != nil {
		return Record{}, fmt.Errorf("recording observation: %w", ierr)
	}
	return rec, nil
}

// where renders f as a SQL predicate and its bind arguments.
func (f Filter) where() (string, []any) {
	var clauses []string
	var args []any

	if f.Class != "" {
		clauses = append(clauses, "class = ?")
		args = append(args, f.Class)
	}
	if f.ScopeKind != "" {
		clauses = append(clauses, "scope_kind = ?")
		args = append(args, f.ScopeKind)
	}
	if f.ScopeRef != "" {
		clauses = append(clauses, "scope_ref = ?")
		args = append(args, f.ScopeRef)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "recorded_at >= ?")
		args = append(args, formatTime(f.Since))
	}

	switch {
	case !f.Before.IsZero() && f.BeforeID != "":
		// The paging cursor. Row-value comparison against the full sort key, so
		// a page boundary that lands inside a cluster of same-millisecond
		// observations resumes within that cluster instead of skipping the rest
		// of it. The observation_by_recorded index serves this directly.
		clauses = append(clauses, "(recorded_at, id) < (?, ?)")
		args = append(args, formatTime(f.Before), f.BeforeID)
	case !f.Before.IsZero():
		// Coarse time bound with no cursor. Correct as a filter; not sufficient
		// for paging, which is why the tools require the pair.
		clauses = append(clauses, "recorded_at < ?")
		args = append(args, formatTime(f.Before))
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// List returns a page of observations newest first, the total size of the match,
// and a per-class tally over that whole match.
//
// All three reads run in one read-only transaction. Without it the page, the
// count, and the tally would each take a different pooled connection and so a
// different WAL snapshot, and a concurrent write landing between them would
// produce a total that disagrees with the page — a caller reasoning over
// numbers no single state of the store ever held. The transaction is opened
// read-only explicitly so it begins with a plain BEGIN rather than acquiring a
// write lock.
//
// total and classes span the whole match rather than the returned page, so they
// stay meaningful when the page is capped.
func (s *Store) List(ctx context.Context, f Filter) (recs []Record, total int, classes []ClassCount, err error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, nil, fmt.Errorf("beginning review: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("closing review: %w", rerr))
		}
	}()

	predicate, args := f.where()

	const columns = `id, recorded_at, class, observation, scope_kind, scope_ref, subject, agent`
	pageQuery := `SELECT ` + columns + ` FROM observation` + predicate +
		` ORDER BY recorded_at DESC, id DESC LIMIT ?`

	rows, qerr := tx.QueryContext(ctx, pageQuery, append(append([]any{}, args...), limit)...)
	if qerr != nil {
		return nil, 0, nil, fmt.Errorf("reading observations: %w", qerr)
	}

	recs = make([]Record, 0, limit)
	for rows.Next() {
		var r Record
		if serr := rows.Scan(
			&r.ID, &r.RecordedAt, &r.Class, &r.Text,
			&r.ScopeKind, &r.ScopeRef, &r.Subject, &r.Agent,
		); serr != nil {
			return nil, 0, nil, errors.Join(
				fmt.Errorf("decoding observation: %w", serr), rows.Close())
		}
		recs = append(recs, r)
	}
	// rows.Err() reports a failure that ended iteration early. Skipping it would
	// silently return a short page, which under concurrency is indistinguishable
	// from data loss.
	if rerr := rows.Err(); rerr != nil {
		return nil, 0, nil, errors.Join(
			fmt.Errorf("reading observations: %w", rerr), rows.Close())
	}
	// The page is fully materialised above, so the rows can be released before
	// the aggregate queries run on the same pinned connection.
	if cerr := rows.Close(); cerr != nil {
		return nil, 0, nil, fmt.Errorf("closing observation rows: %w", cerr)
	}

	countQuery := `SELECT COUNT(*) FROM observation` + predicate
	if cerr := tx.QueryRowContext(ctx, countQuery, args...).Scan(&total); cerr != nil {
		return nil, 0, nil, fmt.Errorf("counting observations: %w", cerr)
	}

	classQuery := `SELECT class, COUNT(*) FROM observation` + predicate +
		` GROUP BY class ORDER BY COUNT(*) DESC, class ASC`
	classRows, gerr := tx.QueryContext(ctx, classQuery, args...)
	if gerr != nil {
		return nil, 0, nil, fmt.Errorf("tallying classes: %w", gerr)
	}
	classes = make([]ClassCount, 0, 8)
	for classRows.Next() {
		var cc ClassCount
		if serr := classRows.Scan(&cc.Class, &cc.Count); serr != nil {
			return nil, 0, nil, errors.Join(
				fmt.Errorf("decoding class tally: %w", serr), classRows.Close())
		}
		classes = append(classes, cc)
	}
	if rerr := classRows.Err(); rerr != nil {
		return nil, 0, nil, errors.Join(
			fmt.Errorf("tallying classes: %w", rerr), classRows.Close())
	}
	if cerr := classRows.Close(); cerr != nil {
		return nil, 0, nil, fmt.Errorf("closing class tally rows: %w", cerr)
	}

	return recs, total, classes, nil
}

// Snapshot writes a consistent single-file copy of the store to dest.
//
// VACUUM INTO runs against a live database and produces a copy with no WAL
// sidecars, which is what makes the result safe to hand to a file-copying backup
// tool. Copying the database file directly can capture a mid-transaction state
// that will not open.
func (s *Store) Snapshot(ctx context.Context, dest string) (err error) {
	// VACUUM INTO refuses to overwrite, so write beside the target and rename.
	// The rename also means a reader of dest never sees a partial snapshot.
	//
	// The intermediate name comes from os.CreateTemp rather than a fixed
	// dest+".tmp" so that two overlapping snapshot runs — a scheduled one and a
	// manual one, say — cannot collide, where a fixed name lets the second run's
	// cleanup delete the first's in-progress output. It is created in dest's own
	// directory so the rename stays on one filesystem and therefore atomic.
	tmp, terr := os.CreateTemp(filepath.Dir(dest), ".field-docket-snapshot-*")
	if terr != nil {
		return fmt.Errorf("reserving snapshot temporary: %w", terr)
	}
	tmpPath := tmp.Name()
	if cerr := tmp.Close(); cerr != nil {
		return errors.Join(
			fmt.Errorf("reserving snapshot temporary: %w", cerr), os.Remove(tmpPath))
	}
	// Removed rather than written into: VACUUM INTO refuses an existing
	// destination. CreateTemp's value here is the reserved unique name, not the
	// file handle.
	if rerr := os.Remove(tmpPath); rerr != nil {
		return fmt.Errorf("clearing snapshot temporary: %w", rerr)
	}

	// Every failure below has to clear the intermediate itself. A unique name is
	// what makes overlapping runs safe, and it is also what removes the fixed
	// name's one virtue: nothing later comes along and clears it. A partial
	// VACUUM INTO left behind would be an orphaned copy of the observations, at
	// whatever mode SQLite chose, in a directory a backup tool may well be
	// watching.
	defer func() {
		if err == nil {
			return
		}
		if rerr := os.Remove(tmpPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("clearing failed snapshot: %w", rerr))
		}
	}()

	if _, verr := s.readDB.ExecContext(ctx, `VACUUM INTO ?`, tmpPath); verr != nil {
		return fmt.Errorf("writing snapshot: %w", verr)
	}

	// SQLite creates a VACUUM INTO destination as a main database, at its own
	// default mode (0644) rather than at the source's — unlike the -wal/-shm
	// sidecars, which do inherit. Without this the backup, the one path designed
	// to carry observations off the machine, would be the widest exposure of a
	// corpus whose only confidentiality boundary is filesystem permissions.
	// Applied before the rename so dest is never briefly world-readable.
	if cerr := os.Chmod(tmpPath, 0o600); cerr != nil {
		return fmt.Errorf("securing snapshot: %w", cerr)
	}
	if rerr := os.Rename(tmpPath, dest); rerr != nil {
		return fmt.Errorf("moving snapshot into place: %w", rerr)
	}
	return nil
}
