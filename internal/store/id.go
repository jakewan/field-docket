package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// newUUIDv7 mints an RFC 9562 version 7 UUID whose leading 48 bits are t as
// Unix milliseconds.
//
// The time prefix earns its place by making ids sort consistently with
// recorded_at, which is what lets the keyset cursor in List use (recorded_at,
// id) as one ordered key. Within a single millisecond the remaining bits are
// random, so ids there are stable and distinct but carry no information about
// which observation was written first — see the ordering note in List.
//
// Hand-rolled rather than pulled from a dependency: it is a dozen lines, and
// the two properties that matter (time-ordered prefix, collision-resistant
// suffix) are worth reading in place.
func newUUIDv7(t time.Time) (string, error) {
	var b [16]byte

	ms := t.UTC().UnixMilli()
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	if _, err := rand.Read(b[6:]); err != nil {
		return "", fmt.Errorf("minting observation id: %w", err)
	}

	// Version 7 in the high nibble of octet 6, RFC 4122 variant in the top two
	// bits of octet 8. Both overwrite random bits, which is why they are applied
	// after the fill rather than before.
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])

	return string(out[:]), nil
}
