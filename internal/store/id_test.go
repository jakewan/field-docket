package store

import (
	"regexp"
	"testing"
	"time"
)

var uuidPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestObservationIDsAreWellFormedUUIDv7(t *testing.T) {
	id, err := newUUIDv7(fixedStart)
	if err != nil {
		t.Fatalf("mint id: %v", err)
	}
	// The pattern pins the version nibble (7) and the variant bits (8-b) as well
	// as the shape, so a mistake in either shows up here.
	if !uuidPattern.MatchString(id) {
		t.Errorf("id %q is not a well-formed UUIDv7", id)
	}
}

// TestObservationIDsSortChronologically covers the property the keyset cursor
// depends on: ids minted at increasing times sort ascending as strings, so
// (recorded_at, id) is one coherent ordered key.
func TestObservationIDsSortChronologically(t *testing.T) {
	var previous string
	base := fixedStart
	for i := range 50 {
		id, err := newUUIDv7(base.Add(time.Duration(i) * time.Millisecond))
		if err != nil {
			t.Fatalf("mint id %d: %v", i, err)
		}
		if previous != "" && id <= previous {
			t.Fatalf("id %q does not sort after %q", id, previous)
		}
		previous = id
	}
}

// TestObservationIDsAreDistinctWithinOneMillisecond is the property that makes
// the cursor usable at a page boundary inside a burst of concurrent writes.
//
// Note what is *not* asserted: ordering. RFC 9562 leaves the sub-millisecond
// bits random, so ids within one millisecond are stable and unique but say
// nothing about which observation was written first.
func TestObservationIDsAreDistinctWithinOneMillisecond(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := range 1000 {
		id, err := newUUIDv7(fixedStart)
		if err != nil {
			t.Fatalf("mint id %d: %v", i, err)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q minted within one millisecond", id)
		}
		seen[id] = true
	}
}

func TestObservationIDsEncodeTheirTimestamp(t *testing.T) {
	// Two instants a day apart must produce ids that sort accordingly, which is
	// what makes the id tiebreak meaningful across milliseconds.
	early, err := newUUIDv7(fixedStart)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	late, err := newUUIDv7(fixedStart.Add(24 * time.Hour))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if early >= late {
		t.Errorf("id for the earlier instant (%q) does not sort before the later one (%q)", early, late)
	}
}
