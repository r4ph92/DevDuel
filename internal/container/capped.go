package container

import "fmt"

// cappedBuffer keeps the beginning and the end of what is written to it and
// counts the rest away.
//
// It never refuses or shortens a write. Docker keeps producing output whether
// or not anyone is listening, and a short write would stall the command being
// read; only the retained bytes cost memory.
//
// Both ends are kept rather than just one. A container that fails at startup
// says so in its first bytes, and the tester's result marker arrives in its
// last, so either end alone would lose something the judge needs.
type cappedBuffer struct {
	headLimit int
	tailLimit int

	head   []byte
	tail   []byte
	elided int
}

func newCappedBuffer(limit int) *cappedBuffer {
	if limit < 2 {
		limit = 2
	}
	return &cappedBuffer{headLimit: limit / 2, tailLimit: limit - limit/2}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)

	if room := b.headLimit - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		// Compact only once the tail has grown to twice what is kept, so a
		// long stream of small writes does not recopy it on every one. The
		// slack means the buffer holds at most 1.5x the limit at any moment.
		if len(b.tail) > 2*b.tailLimit {
			b.trim()
		}
	}
	return n, nil
}

// trim drops the oldest bytes of the tail beyond what is kept.
func (b *cappedBuffer) trim() {
	if len(b.tail) <= b.tailLimit {
		return
	}
	drop := len(b.tail) - b.tailLimit
	b.elided += drop
	b.tail = append(b.tail[:0], b.tail[drop:]...)
}

func (b *cappedBuffer) String() string {
	b.trim()
	if b.elided == 0 {
		return string(b.head) + string(b.tail)
	}
	return fmt.Sprintf("%s\n... %d bytes elided ...\n%s", b.head, b.elided, b.tail)
}

// Elided is how many bytes were dropped from the middle.
func (b *cappedBuffer) Elided() int {
	b.trim()
	return b.elided
}
