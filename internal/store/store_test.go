package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecordedAtIsUTCRegardlessOfLocalZone guards the timestamp layout's
// trailing Z, which is a literal character rather than a zone token.
//
// Formatting a local time with that layout stamps local wall-clock time wearing
// a UTC label. The damage is permanent — the store is append-only — and shows up
// as cross-machine comparisons off by the local offset, plus non-monotonic
// ordering across a DST transition. A spec run in UTC would never catch it, so
// this one forces a zone with a large offset.
func TestRecordedAtIsUTCRegardlessOfLocalZone(t *testing.T) {
	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("zone database unavailable: %v", err)
	}

	// An instant whose local rendering differs from its UTC rendering.
	instant := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	st, err := Open(context.Background(),
		filepath.Join(t.TempDir(), "field-docket.db"),
		WithClock(func() time.Time { return instant.In(zone) }),
	)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})

	rec, err := st.Append(context.Background(), sample("zones"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	const want = "2026-03-14T09:26:53.000Z"
	if rec.RecordedAt != want {
		t.Errorf("recorded_at = %q, want %q: the clock's zone leaked into storage",
			rec.RecordedAt, want)
	}

	// Round-trips as the instant it claims to be.
	parsed, perr := time.Parse(time.RFC3339, rec.RecordedAt)
	if perr != nil {
		t.Fatalf("stored timestamp does not parse as RFC 3339: %v", perr)
	}
	if !parsed.Equal(instant) {
		t.Errorf("stored timestamp is %v, want %v", parsed, instant)
	}
}

func TestStoredTimestampsAreFixedWidth(t *testing.T) {
	// Fixed width is what makes lexicographic ordering equal chronological
	// ordering, which the since/before filters rely on. A whole second and a
	// fractional one must render to the same length.
	whole := formatTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	fractional := formatTime(time.Date(2026, 1, 2, 3, 4, 5, 120*int(time.Millisecond), time.UTC))

	if len(whole) != len(fractional) {
		t.Errorf("widths differ: %q (%d) vs %q (%d)", whole, len(whole), fractional, len(fractional))
	}
	if whole >= fractional {
		t.Errorf("%q should sort before %q", whole, fractional)
	}
}

func TestListOrdersMostRecentFirst(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for i := range 5 {
		if _, err := st.Append(ctx, sample("ordering")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	recs, _, _, err := st.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i-1].RecordedAt < recs[i].RecordedAt {
			t.Errorf("record %d is older than %d", i-1, i)
		}
	}
}

func TestListDefaultsItsLimit(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for range defaultLimit + 10 {
		if _, err := st.Append(ctx, sample("capping")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	recs, total, _, err := st.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != defaultLimit {
		t.Errorf("returned %d records, want the default limit of %d", len(recs), defaultLimit)
	}
	if total != defaultLimit+10 {
		t.Errorf("total = %d, want %d: total spans the match, not the page", total, defaultLimit+10)
	}
}

func TestStoreFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "field-docket.db")
	st := openAt(t, path)

	if _, err := st.Append(context.Background(), sample("permissions")); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Observations are free text composed from session context, so the mode has
	// to be right at creation — SQLite stamps the WAL sidecars with the main
	// file's mode when it creates them, before any later chmod could run.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("store mode = %04o, want no group or other access", perm)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Logf("store directory mode = %04o", perm)
	}
}

func TestSnapshotProducesAnOpenableCopy(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for range 5 {
		if _, err := st.Append(ctx, sample("snapshot")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	dest := filepath.Join(t.TempDir(), "snapshot.db")
	if err := st.Snapshot(ctx, dest); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Checked before anything opens the copy: opening it in WAL mode would
	// create the sidecars itself and the assertion would be meaningless.
	// VACUUM INTO writes a single settled file, which is what makes the result
	// safe to hand to a file-copying backup tool.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, serr := os.Stat(dest + suffix); serr == nil {
			t.Errorf("snapshot left a %s sidecar", suffix)
		}
	}

	// The point of the snapshot is that a backup tool can move it and it will
	// still open. Copying the live database can capture a mid-transaction state
	// that will not.
	copied := openAt(t, dest)
	_, total, _, err := copied.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("list from snapshot: %v", err)
	}
	if total != 5 {
		t.Errorf("snapshot holds %d observations, want 5", total)
	}
}

func TestSnapshotOverwritesAnEarlierSnapshot(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "snapshot.db")

	if _, err := st.Append(ctx, sample("first")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.Snapshot(ctx, dest); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}

	if _, err := st.Append(ctx, sample("second")); err != nil {
		t.Fatalf("append: %v", err)
	}
	// VACUUM INTO refuses an existing destination, so a scheduled backup would
	// fail from the second run onward without the write-and-rename.
	if err := st.Snapshot(ctx, dest); err != nil {
		t.Fatalf("second snapshot over an existing file: %v", err)
	}

	copied := openAt(t, dest)
	_, total, _, err := copied.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Errorf("re-snapshot holds %d observations, want 2", total)
	}
}

func TestAppendRejectsNothingOnAPopulatedStore(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for range 100 {
		if _, err := st.Append(ctx, sample("bulk")); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// The store half of the recording/adjudication separation: Append takes no
	// state argument and runs no query, so contents cannot influence writability.
	if _, err := st.Append(ctx, sample("bulk")); err != nil {
		t.Errorf("append against a populated store: %v", err)
	}
}

func TestFilterCombinesPredicatesWithAnd(t *testing.T) {
	f := Filter{Class: "correctness", ScopeKind: "project", ScopeRef: "jakewan/field-docket"}
	clause, args := f.where()

	if strings.Count(clause, " AND ") != 2 {
		t.Errorf("clause = %q, want two AND joins", clause)
	}
	if len(args) != 3 {
		t.Errorf("bound %d args, want 3", len(args))
	}
}

func TestEmptyFilterBindsNothing(t *testing.T) {
	clause, args := Filter{}.where()
	if clause != "" {
		t.Errorf("clause = %q, want empty", clause)
	}
	if len(args) != 0 {
		t.Errorf("bound %d args, want 0", len(args))
	}
}
