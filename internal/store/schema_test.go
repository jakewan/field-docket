package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedStart is the instant the test clock begins at.
var fixedStart = time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)

// advancingClock returns a clock that moves forward by step on every call, so
// ordering assertions are deterministic. See WithClock for why the wall clock
// will not do.
//
// Guarded by a mutex because Append calls the clock, and concurrent recorders
// call Append from several goroutines — an unsynchronised counter here trips the
// race detector inside the very specs that exercise concurrency.
func advancingClock(step time.Duration) func() time.Time {
	var mu sync.Mutex
	current := fixedStart
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := current
		current = current.Add(step)
		return t
	}
}

// newStore opens a store under t.TempDir() with a deterministic clock.
func newStore(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "field-docket.db"))
}

// openAt opens a store at an explicit path, registering cleanup.
//
// Closing matters beyond tidiness: t.TempDir() fails the test if it cannot
// remove the directory, and an open handle can prevent that.
func openAt(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(context.Background(), path, WithClock(advancingClock(time.Second)))
	if err != nil {
		t.Fatalf("open store at %s: %v", path, err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	return st
}

// namedHandle pairs a connection pool with the label a failure should name.
type namedHandle struct {
	name string
	db   *sql.DB
}

// handles exposes both pools so a spec can assert a pragma landed on each. The
// pragmas are carried in the DSN, and the two DSNs differ, so checking only one
// would miss a regression on the other.
func (s *Store) handles() []namedHandle {
	return []namedHandle{{"read", s.readDB}, {"write", s.writeDB}}
}

// sample is a minimal valid observation for specs that only need a row to exist.
func sample(class string) Observation {
	return Observation{
		Class:     class,
		Text:      "An observation.",
		ScopeKind: "project",
		ScopeRef:  "jakewan/field-docket",
	}
}

func TestOpenCreatesSchemaOnFirstRun(t *testing.T) {
	st := newStore(t)

	var version int
	if err := st.readDB.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}

	if _, err := st.Append(context.Background(), sample("smoke")); err != nil {
		t.Errorf("append against a freshly created schema: %v", err)
	}
}

func TestOpenIsIdempotentOnAnExistingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "field-docket.db")

	first := openAt(t, path)
	if _, err := first.Append(context.Background(), sample("first")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := openAt(t, path)
	_, total, _, err := second.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1: reopening must not recreate or clear the schema", total)
	}
}

// TestOpenEnablesWriteAheadLogging and TestOpenAppliesBusyTimeout look like
// implementation tests but are not: both claims are load-bearing for concurrent
// access, and a silent regression in either would surface as data unavailability
// under load rather than as an obvious failure.
func TestOpenEnablesWriteAheadLogging(t *testing.T) {
	st := newStore(t)

	for _, h := range st.handles() {
		var mode string
		if err := h.db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatalf("%s handle: read journal_mode: %v", h.name, err)
		}
		if !strings.EqualFold(mode, "wal") {
			t.Errorf("%s handle journal_mode = %q, want wal", h.name, mode)
		}
	}
}

func TestOpenAppliesBusyTimeout(t *testing.T) {
	st := newStore(t)

	for _, h := range st.handles() {
		var ms int
		if err := h.db.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&ms); err != nil {
			t.Fatalf("%s handle: read busy_timeout: %v", h.name, err)
		}
		if ms != 5000 {
			t.Errorf("%s handle busy_timeout = %d, want 5000", h.name, ms)
		}
	}
}

// TestObservationCarriesNoAdjudicationState is the schema half of the
// recording/adjudication separation.
//
// The assertion is set-equality against the recording columns, so adding any
// column — adjudicated_at, resolved_at, a disposition — fails here. That is the
// intended outcome: adjudication state belongs on a future adjudication entity
// which references observations, never on the observation row itself.
func TestObservationCarriesNoAdjudicationState(t *testing.T) {
	st := newStore(t)

	rows, err := st.readDB.QueryContext(context.Background(), `PRAGMA table_info(observation)`)
	if err != nil {
		t.Fatalf("read table_info: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("close table_info rows: %v", cerr)
		}
	}()

	var got []string
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			defaultVal any
			pk         int
		)
		if serr := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); serr != nil {
			t.Fatalf("scan table_info: %v", serr)
		}
		got = append(got, name)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("iterate table_info: %v", rerr)
	}

	want := []string{
		"id", "recorded_at", "class", "observation",
		"scope_kind", "scope_ref", "subject", "agent",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("observation columns = %v, want %v\n"+
			"A column referring to any judgment about an observation breaks the "+
			"recording/adjudication separation; adjudication references "+
			"observations, never the reverse.", got, want)
	}
}

func TestRecordedObservationsCannotBeUpdated(t *testing.T) {
	st := newStore(t)
	rec, err := st.Append(context.Background(), sample("immutable"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	_, uerr := st.writeDB.ExecContext(context.Background(),
		`UPDATE observation SET class = ? WHERE id = ?`, "rewritten", rec.ID)
	if uerr == nil {
		t.Fatal("an observation was updated; the append-only trigger did not fire")
	}
	if !strings.Contains(uerr.Error(), "append-only") {
		t.Errorf("update failed for the wrong reason: %v", uerr)
	}
}

func TestRecordedObservationsCannotBeDeleted(t *testing.T) {
	st := newStore(t)
	rec, err := st.Append(context.Background(), sample("immutable"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	_, derr := st.writeDB.ExecContext(context.Background(),
		`DELETE FROM observation WHERE id = ?`, rec.ID)
	if derr == nil {
		t.Fatal("an observation was deleted; the append-only trigger did not fire")
	}
	if !strings.Contains(derr.Error(), "append-only") {
		t.Errorf("delete failed for the wrong reason: %v", derr)
	}
}
