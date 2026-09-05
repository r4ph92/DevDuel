package container

import (
	"strings"
	"testing"
)

func TestCappedBufferKeepsShortOutputWhole(t *testing.T) {
	b := newCappedBuffer(1024)

	for _, s := range []string{"first ", "second ", "third"} {
		if _, err := b.Write([]byte(s)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	if got, want := b.String(), "first second third"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if b.Elided() != 0 {
		t.Errorf("Elided() = %d, want 0", b.Elided())
	}
}

func TestCappedBufferKeepsBothEndsOfAFlood(t *testing.T) {
	const limit = 1000
	b := newCappedBuffer(limit)

	head := "START-OF-OUTPUT"
	tail := "END-OF-OUTPUT"
	b.mustWrite(t, head)
	for range 500 {
		b.mustWrite(t, strings.Repeat("x", 100))
	}
	b.mustWrite(t, tail)

	got := b.String()
	if !strings.HasPrefix(got, head) {
		t.Errorf("output should start with %q, got %q", head, got[:min(len(got), 40)])
	}
	if !strings.HasSuffix(got, tail) {
		t.Errorf("output should end with %q, got %q", tail, got[max(0, len(got)-40):])
	}
	if b.Elided() == 0 {
		t.Error("Elided() = 0, want the dropped middle to be counted")
	}
	if !strings.Contains(got, "bytes elided") {
		t.Errorf("output should say what was dropped, got %q", got[:min(len(got), 200)])
	}
}

func TestCappedBufferBoundsWhatItRetains(t *testing.T) {
	const limit = 1000
	b := newCappedBuffer(limit)

	for range 1000 {
		b.mustWrite(t, strings.Repeat("y", 1000))
	}

	// The retained text is the two kept halves plus the one-line notice.
	if got := len(b.String()); got > limit+64 {
		t.Errorf("String() kept %d bytes, want no more than %d", got, limit+64)
	}
	if got, want := b.Elided(), 1000*1000-limit; got > want+64 {
		t.Errorf("Elided() = %d, want about %d", got, want)
	}
}

func TestCappedBufferCountsEveryByteWritten(t *testing.T) {
	b := newCappedBuffer(16)

	n, err := b.Write([]byte(strings.Repeat("z", 4096)))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 4096 {
		t.Errorf("Write returned %d, want 4096 — a short write would stall the pipe", n)
	}
}

func (b *cappedBuffer) mustWrite(t *testing.T, s string) {
	t.Helper()
	if _, err := b.Write([]byte(s)); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func TestCappedBufferSurvivesAnAbsurdLimit(t *testing.T) {
	for _, limit := range []int{-1, 0, 1} {
		b := newCappedBuffer(limit)
		b.mustWrite(t, "some output that will not fit")

		if got := len(b.String()); got > 64 {
			t.Errorf("limit %d kept %d bytes, want almost nothing", limit, got)
		}
	}
}
