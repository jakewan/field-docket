package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Environment variables that switch the test binary into child-writer mode.
const (
	envWriterDB    = "FIELD_DOCKET_TEST_WRITER_DB"
	envWriterTag   = "FIELD_DOCKET_TEST_WRITER_TAG"
	envWriterCount = "FIELD_DOCKET_TEST_WRITER_COUNT"
)

// TestMain re-enters the test binary as a plain writer process when the writer
// environment is set.
//
// This is what makes a genuine multi-process spec possible: goroutines share one
// SQLite connection pool and one process, so they exercise none of the POSIX
// file locking, WAL contention, or busy-handler behaviour the store's whole
// design rests on. The child exits before m.Run(), so it runs no tests and
// contributes no coverage.
func TestMain(m *testing.M) {
	if path := os.Getenv(envWriterDB); path != "" {
		os.Exit(runWriterChild(path, os.Getenv(envWriterTag), os.Getenv(envWriterCount)))
	}
	os.Exit(m.Run())
}

// runWriterChild opens the store and appends count observations tagged with tag.
func runWriterChild(path, tag, rawCount string) int {
	count, err := strconv.Atoi(rawCount)
	if err != nil {
		fmt.Fprintf(os.Stderr, "writer %s: bad count %q: %v\n", tag, rawCount, err)
		return 2
	}

	ctx := context.Background()
	st, err := Open(ctx, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "writer %s: open: %v\n", tag, err)
		return 1
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "writer %s: close: %v\n", tag, cerr)
		}
	}()

	for i := range count {
		if _, aerr := st.Append(ctx, Observation{
			Class:     tag,
			Text:      fmt.Sprintf("observation %d from %s", i, tag),
			ScopeKind: "user",
		}); aerr != nil {
			fmt.Fprintf(os.Stderr, "writer %s: append %d: %v\n", tag, i, aerr)
			return 1
		}
	}
	return 0
}

// TestConcurrentRecordersLoseNoObservations covers the in-process case: many
// goroutines against one Store.
//
// Assertions are on the set of ids, never on order. Concurrent writers share
// milliseconds, and ids tie-break randomly within one, so any positional
// assertion here would be a race rather than a check.
func TestConcurrentRecordersLoseNoObservations(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	const writers = 8
	const perWriter = 25

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	ids := make(chan string, writers*perWriter)

	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				rec, err := st.Append(ctx, Observation{
					Class:     fmt.Sprintf("writer-%d", w),
					Text:      fmt.Sprintf("observation %d", i),
					ScopeKind: "user",
				})
				if err != nil {
					errs <- err
					return
				}
				ids <- rec.ID
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	close(ids)

	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}

	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate observation id %s", id)
		}
		seen[id] = true
	}
	if len(seen) != writers*perWriter {
		t.Errorf("minted %d distinct ids, want %d", len(seen), writers*perWriter)
	}

	_, total, _, err := st.List(ctx, Filter{Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != writers*perWriter {
		t.Errorf("stored %d observations, want %d", total, writers*perWriter)
	}
}

// TestSeparateProcessesRecordConcurrentlyWithoutLoss is the spec the storage
// choice exists for.
//
// Several OS processes hold their own connections to one store file and append
// at the same time, which is the arrangement in real use: each agent session
// runs its own server. Only this spec exercises cross-process locking, WAL, and
// the busy handler; the in-process version above proves none of them.
func TestSeparateProcessesRecordConcurrentlyWithoutLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns child processes")
	}

	path := filepath.Join(t.TempDir(), "field-docket.db")
	ctx := context.Background()

	const children = 4
	const perChild = 30

	var wg sync.WaitGroup
	failures := make(chan error, children)

	for c := range children {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, os.Args[0])
			cmd.Env = append(childEnv(),
				envWriterDB+"="+path,
				envWriterTag+"="+fmt.Sprintf("child-%d", c),
				envWriterCount+"="+strconv.Itoa(perChild),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				failures <- fmt.Errorf("child %d: %w: %s", c, err, out)
			}
		}(c)
	}
	// Every child must be reaped before the assertions run, and before
	// t.TempDir()'s cleanup tries to remove a directory a child may still hold
	// open.
	wg.Wait()
	close(failures)

	for err := range failures {
		t.Fatalf("%v", err)
	}

	st := openAt(t, path)
	recs, total, classes, err := st.List(ctx, Filter{Limit: children * perChild})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if total != children*perChild {
		t.Errorf("stored %d observations, want %d: writes were lost across processes",
			total, children*perChild)
	}

	ids := map[string]bool{}
	for _, r := range recs {
		if ids[r.ID] {
			t.Errorf("duplicate id %s across processes", r.ID)
		}
		ids[r.ID] = true
	}

	// Every child must be represented, so a single child silently failing to
	// write cannot be masked by the others' rows.
	if len(classes) != children {
		t.Errorf("observations came from %d children, want %d", len(classes), children)
	}
	for _, cc := range classes {
		if cc.Count != perChild {
			t.Errorf("%s wrote %d observations, want %d", cc.Class, cc.Count, perChild)
		}
	}
}

// childEnv returns the parent environment with coverage instrumentation
// stripped, so re-executed children do not write into the parent's coverage
// directory.
func childEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOCOVERDIR=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// TestConcurrentReviewDoesNotBlockRecord pins WAL's specific promise: a reader
// does not exclude a writer.
//
// Without WAL — or with _txlock=immediate wrongly applied to the read handle —
// the read transaction would hold a lock that stalls the append until the busy
// timeout. The deadline is far below that 5s timeout, so a regression shows up
// as a failure rather than as a slow test.
func TestConcurrentReviewDoesNotBlockRecord(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for range 20 {
		if _, err := st.Append(ctx, sample("seed")); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Hold a read transaction open for the duration of the append.
	tx, err := st.readDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	var seen int
	if serr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM observation`).Scan(&seen); serr != nil {
		t.Fatalf("read inside transaction: %v", serr)
	}

	done := make(chan error, 1)
	go func() {
		_, aerr := st.Append(ctx, sample("concurrent"))
		done <- aerr
	}()

	select {
	case aerr := <-done:
		if aerr != nil {
			t.Errorf("append while a read transaction was open: %v", aerr)
		}
	case <-time.After(2 * time.Second):
		t.Error("append blocked behind an open read transaction; " +
			"WAL should let a reader and a writer proceed together")
	}

	if rerr := tx.Rollback(); rerr != nil {
		t.Errorf("rollback: %v", rerr)
	}
}

// TestReviewCountsAgreeWithPageUnderConcurrentWrites pins the one read
// transaction List runs all three of its queries inside.
//
// The page, the total, and the class tally are three separate queries. On three
// pooled connections they would take three WAL snapshots, so an append landing
// between them yields a total that disagrees with the tally — a grooming pass
// would then reason about a state the store never actually had. That is
// invisible to every single-writer test, which is why this one records
// throughout.
//
// The assertions are relational rather than absolute: how many appends land
// during the read is genuinely nondeterministic, so pinning a count would be a
// flake. What must hold is that the three results describe one instant.
func TestReviewCountsAgreeWithPageUnderConcurrentWrites(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for range 30 {
		if _, err := st.Append(ctx, sample("seed")); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
				if _, aerr := st.Append(ctx, sample("concurrent")); aerr != nil {
					done <- aerr
					return
				}
			}
		}
	}()

	for range 40 {
		recs, total, classes, err := st.List(ctx, Filter{Limit: 10})
		if err != nil {
			close(stop)
			<-done
			t.Fatalf("list under concurrent writes: %v", err)
		}

		var tallied int
		for _, c := range classes {
			tallied += c.Count
		}
		if tallied != total {
			close(stop)
			<-done
			t.Fatalf("class counts sum to %d but total is %d: "+
				"the tally and the count came from different snapshots", tallied, total)
		}
		if len(recs) > total {
			close(stop)
			<-done
			t.Fatalf("page holds %d rows but total is %d: "+
				"the page came from a later snapshot than the count", len(recs), total)
		}
	}

	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("concurrent append: %v", err)
	}
}

// TestConcurrentOpensAgreeOnSchemaVersion covers the cold-start race: several
// sessions starting at once against a store that does not exist yet.
//
// Each would read user_version 0 and try to run the ladder. The migration's
// BEGIN IMMEDIATE is what serialises them; a deferred transaction would let them
// all proceed and collide on CREATE TABLE.
func TestConcurrentOpensAgreeOnSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "field-docket.db")
	ctx := context.Background()

	const openers = 6
	var wg sync.WaitGroup
	errs := make(chan error, openers)
	stores := make(chan *Store, openers)

	for range openers {
		wg.Go(func() {
			st, err := Open(ctx, path)
			if err != nil {
				errs <- err
				return
			}
			stores <- st
		})
	}
	wg.Wait()
	close(errs)
	close(stores)

	for err := range errs {
		t.Errorf("concurrent open: %v", err)
	}

	var opened []*Store
	for st := range stores {
		opened = append(opened, st)
	}
	t.Cleanup(func() {
		for _, st := range opened {
			if cerr := st.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		}
	})

	if len(opened) == 0 {
		t.Fatal("no store opened successfully")
	}

	var version int
	if err := opened[0].readDB.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}

	// Exactly one observation table, and it works.
	var tables int
	if err := opened[0].readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'observation'`,
	).Scan(&tables); err != nil {
		t.Fatalf("count observation tables: %v", err)
	}
	if tables != 1 {
		t.Errorf("found %d observation tables, want 1", tables)
	}
}
