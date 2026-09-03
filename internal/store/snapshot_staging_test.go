package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSnapshotNeverExposesTheIntermediate samples the destination directory's
// top-level entries for the whole of a snapshot and fails if any of them is
// reachable by anyone but its owner.
//
// It reads that directory only, not into it, so what it actually checks is that
// nothing the snapshot puts beside dest — the staging directory, and under the
// previous arrangement the bare intermediate — is group- or other-accessible. A
// 0700 staging directory makes its contents unreachable, so this is sufficient
// for the property, but it is not a recursive scan and does not claim to be.
//
// The intermediate is the exposure that matters: SQLite creates a VACUUM INTO
// destination at its own default mode adjusted by the umask, and the chmod that
// tightens it cannot run until the copy is complete. So for the duration of the
// write there is a full copy of the corpus on disk at whatever mode the umask
// allowed — which is why the intermediate is staged inside a directory that
// denies group and other rather than written beside dest.
//
// The poll samples rather than proves: it can miss a window it never happens to
// observe, so a green run is weaker evidence than a red one. It is sized to make
// that unlikely — the store is loaded so the write takes long enough to sample
// many times — and it was confirmed to fail against the previous arrangement
// before being trusted.
func TestSnapshotNeverExposesTheIntermediate(t *testing.T) {
	requirePOSIXModes(t)

	st := newStore(t)
	ctx := context.Background()
	for range 2000 {
		if _, err := st.Append(ctx, sample("bulk")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "snapshot.db")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var exposed []string

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Paced rather than spinning: this runs alongside the snapshot it is
			// measuring, under -race, on CI runners with few cores, and an
			// unthrottled ReadDir loop would contend with the work under test.
			// The interval is far shorter than the write it samples.
			time.Sleep(200 * time.Microsecond)

			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				info, ierr := e.Info()
				if ierr != nil {
					continue
				}
				if perm := info.Mode().Perm(); perm&0o077 != 0 {
					mu.Lock()
					exposed = append(exposed, e.Name())
					mu.Unlock()
				}
			}
		}
	})

	serr := st.Snapshot(ctx, dest)
	close(stop)
	wg.Wait()

	if serr != nil {
		t.Fatalf("snapshot: %v", serr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(exposed) != 0 {
		t.Errorf("observed %v reachable beyond its owner during the snapshot", exposed[:min(len(exposed), 3)])
	}
}
