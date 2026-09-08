package id

import (
	"strings"
	"testing"
	"time"
)

func TestNewSetsVersionAndVariant(t *testing.T) {
	u := New()

	if got := u[6] >> 4; got != 7 {
		t.Errorf("version = %d, want 7", got)
	}
	if got := u[8] >> 6; got != 0b10 {
		t.Errorf("variant = %02b, want 10", got)
	}
	if u.IsNil() {
		t.Error("New returned the nil id")
	}
}

func TestNewCarriesTheMintingTime(t *testing.T) {
	when := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	if got := newAt(when).Time(); !got.Equal(when) {
		t.Errorf("Time() = %s, want %s", got, when)
	}
}

// Ids minted in order must sort in order, because that ordering is the only
// reason to prefer version 7 over version 4.
func TestIdsMintedInOrderSortInOrder(t *testing.T) {
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	var prev string
	for i := range 100 {
		got := newAt(base.Add(time.Duration(i) * time.Millisecond)).String()
		if got <= prev {
			t.Fatalf("id %d sorts at or before its predecessor: %s <= %s", i, got, prev)
		}
		prev = got
	}
}

func TestNewIsNotRepeatable(t *testing.T) {
	seen := make(map[ID]bool, 1000)
	for range 1000 {
		u := New()
		if seen[u] {
			t.Fatalf("New returned %s twice", u)
		}
		seen[u] = true
	}
}

func TestStringParseRoundTrip(t *testing.T) {
	want := New()

	got, err := Parse(want.String())
	if err != nil {
		t.Fatalf("Parse(%q): %v", want, err)
	}
	if got != want {
		t.Errorf("Parse round trip = %s, want %s", got, want)
	}
}

func TestStringIsCanonical(t *testing.T) {
	s := New().String()

	if len(s) != 36 {
		t.Fatalf("String() = %q, want 36 characters", s)
	}
	if got := strings.Count(s, "-"); got != 4 {
		t.Errorf("String() = %q, want 4 hyphens", s)
	}
	if strings.ToLower(s) != s {
		t.Errorf("String() = %q, want lowercase hex", s)
	}
}

func TestParseRejectsAnythingButTheCanonicalForm(t *testing.T) {
	canonical := New().String()

	for name, in := range map[string]string{
		"empty":      "",
		"too short":  canonical[:35],
		"too long":   canonical + "0",
		"braced":     "{" + canonical + "}",
		"urn":        "urn:uuid:" + canonical,
		"unhyphened": strings.ReplaceAll(canonical, "-", "") + "0000",
		"not hex":    "zzzzzzzz" + canonical[8:],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(in); err == nil {
				t.Errorf("Parse(%q) succeeded, want an error", in)
			}
		})
	}
}

func TestTextRoundTrip(t *testing.T) {
	want := New()

	text, err := want.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}

	var got ID
	if err := got.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText(%q): %v", text, err)
	}
	if got != want {
		t.Errorf("text round trip = %s, want %s", got, want)
	}
}
