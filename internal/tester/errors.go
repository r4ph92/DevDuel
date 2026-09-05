package tester

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoResults reports output with no result document in it at all.
//
// This is not a broken tester — a tester that was killed before it could
// report says exactly this — so [Collect] absorbs it into a complete set of
// errors rather than failing the job.
var ErrNoResults = errors.New("no " + Marker + " marker in tester output")

// ProtocolError is a result document this package could not read.
//
// It means the challenge's tester is wrong, not that the player failed, so it
// fails the job instead of being scored. `devduelctl challenge verify` is
// where this is meant to surface, long before a match.
type ProtocolError struct {
	// Reasons is everything wrong with the document, so whoever is fixing the
	// tester sees the whole list at once.
	Reasons []string
	// Excerpt is the beginning of the document as the judge received it.
	Excerpt string
}

func (e *ProtocolError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "unreadable result document: %s", plural(len(e.Reasons), "problem"))
	for _, r := range e.Reasons {
		fmt.Fprintf(&b, "\n  %s", r)
	}
	if e.Excerpt != "" {
		fmt.Fprintf(&b, "\ndocument began: %s", e.Excerpt)
	}
	return b.String()
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// excerpt is the first few hundred characters of s on one line, for showing
// an author what the judge actually saw.
func excerpt(s string) string {
	const limit = 200

	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
