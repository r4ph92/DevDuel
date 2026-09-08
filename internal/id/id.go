// Package id mints the identifiers DevDuel stores in uuid columns.
//
// Every id is a UUID version 7: 48 bits of Unix milliseconds followed by
// randomness. The timestamp prefix is the entire point. Version 4 ids arrive
// in random order, so every insert lands on a random leaf of the primary key
// index and the index stops fitting in cache long before the table does.
// Version 7 ids arrive in roughly the order they are created, so inserts stay
// near the right edge of the index.
//
// Ids are minted here rather than by the database so that a caller holds the
// id before the row exists. Inserting a match and its two players in one
// transaction needs the match id up front, and a RETURNING round trip per row
// is a worse trade than 16 bytes of local randomness.
//
// Ordering is only to the millisecond. Two ids minted in the same millisecond
// sort arbitrarily, so nothing may treat an id as a sequence number. The
// per-match sequence in match_events exists precisely because ids cannot do
// that job.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ID is a UUID. The zero value is the nil UUID, which is never minted and
// which no column accepts as a foreign key.
type ID [16]byte

// Nil is the all-zero UUID, used to mean "no id" where a bare ID is returned.
var Nil ID

// New returns a fresh version 7 UUID.
func New() ID { return newAt(time.Now()) }

// newAt is New with the clock injected, so tests can pin the timestamp.
func newAt(t time.Time) ID {
	var u ID

	// Bytes 0 through 5 are the timestamp, big endian milliseconds.
	ms := t.UnixMilli()
	u[0] = byte(ms >> 40)
	u[1] = byte(ms >> 32)
	u[2] = byte(ms >> 24)
	u[3] = byte(ms >> 16)
	u[4] = byte(ms >> 8)
	u[5] = byte(ms)

	// crypto/rand.Read never fails: as of Go 1.24 it panics rather than
	// returning an error, so there is nothing here to handle.
	_, _ = rand.Read(u[6:])

	// Version 7 in the high nibble of byte 6, RFC 9562 variant in the top
	// two bits of byte 8. Both overwrite randomness that was just written.
	u[6] = (u[6] & 0x0f) | 0x70
	u[8] = (u[8] & 0x3f) | 0x80

	return u
}

// Time is the millisecond the id was minted, meaningful only for a version 7
// id. It is here for debugging: an id in a log line says when its row was
// created without joining anything.
func (u ID) Time() time.Time {
	ms := int64(u[0])<<40 | int64(u[1])<<32 | int64(u[2])<<24 |
		int64(u[3])<<16 | int64(u[4])<<8 | int64(u[5])
	return time.UnixMilli(ms).UTC()
}

// IsNil reports whether u is the zero id.
func (u ID) IsNil() bool { return u == Nil }

// String renders the canonical 8-4-4-4-12 form.
func (u ID) String() string {
	var buf [36]byte
	hex.Encode(buf[0:8], u[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], u[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], u[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], u[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], u[10:16])
	return string(buf[:])
}

// ErrSyntax means the text was not a canonical UUID.
var ErrSyntax = errors.New("id: not a uuid")

// Parse reads the canonical 8-4-4-4-12 form. It accepts nothing else: the
// braced and urn: spellings exist in the wild, and accepting them would mean
// the same id has several representations in URLs and logs.
func Parse(s string) (ID, error) {
	var u ID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return Nil, fmt.Errorf("%w: %q", ErrSyntax, s)
	}

	groups := [...]struct{ dst, lo, hi int }{
		{0, 0, 8}, {4, 9, 13}, {6, 14, 18}, {8, 19, 23}, {10, 24, 36},
	}
	for _, g := range groups {
		if _, err := hex.Decode(u[g.dst:], []byte(s[g.lo:g.hi])); err != nil {
			return Nil, fmt.Errorf("%w: %q", ErrSyntax, s)
		}
	}
	return u, nil
}

// MarshalText renders the canonical form, so an ID inside a JSON payload or a
// query parameter looks like a UUID and not like an array of bytes.
func (u ID) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

// UnmarshalText reads the canonical form.
func (u *ID) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}
	*u = parsed
	return nil
}
