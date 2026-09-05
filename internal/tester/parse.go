package tester

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// statusList is how the valid statuses are named back to an author.
const statusList = `"pass", "fail" or "error"`

// wireDocument is the JSON that follows the marker.
type wireDocument struct {
	Schema  string       `json:"schema"`
	Results []wireResult `json:"results"`
}

type wireResult struct {
	Key        string `json:"key"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message"`
}

// Parse reads the result document out of tester output, without reconciling
// it against any challenge.
//
// It returns [ErrNoResults] when there is no marker, and a *[ProtocolError]
// when there is one but the document behind it cannot be read.
func Parse(stdout string) ([]Result, error) { return parse(stdout, nil) }

// parse reads the document. When declared is non-nil, a result for a key that
// is not in it is a problem — only a caller holding the challenge spec knows
// which keys exist.
func parse(stdout string, declared map[string]bool) ([]Result, error) {
	doc, ok := documentAfterMarker(stdout)
	if !ok {
		return nil, ErrNoResults
	}
	if strings.TrimSpace(doc) == "" {
		return nil, &ProtocolError{Reasons: []string{"there is nothing after the " + Marker + " marker"}}
	}

	// The tester's own runner prints its summary after the plugin has written
	// the document, so a decoder that reads one value and stops is what this
	// needs — not a whole-input unmarshal.
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.DisallowUnknownFields()

	var wire wireDocument
	if err := dec.Decode(&wire); err != nil {
		return nil, &ProtocolError{Reasons: []string{decodeReason(err)}, Excerpt: excerpt(doc)}
	}

	var p problems
	if wire.Schema == "" {
		p.add("the document declares no schema; this judge reads %q", Schema)
	} else if wire.Schema != Schema {
		p.add("the document declares schema %q, but this judge reads %q", wire.Schema, Schema)
	}

	results := make([]Result, 0, len(wire.Results))
	seen := make(map[string]int, len(wire.Results))

	for i, r := range wire.Results {
		field := func(name string) string { return fmt.Sprintf("results[%d].%s", i, name) }

		switch {
		case r.Key == "":
			p.add("%s: is required", field("key"))
		case declared != nil && !declared[r.Key]:
			p.add("%s: %q is not a declared requirement of this challenge", field("key"), r.Key)
		default:
			if first, dup := seen[r.Key]; dup {
				p.add("%s: %q was reported twice, already at results[%d]", field("key"), r.Key, first)
			} else {
				seen[r.Key] = i
			}
		}

		switch status := Status(r.Status); status {
		case StatusPass, StatusFail, StatusError:
		case "":
			p.add("%s: is required, one of %s", field("status"), statusList)
		default:
			p.add("%s: %q is not a status; use %s", field("status"), r.Status, statusList)
		}

		if r.DurationMS < 0 {
			p.add("%s: must not be negative, got %d", field("duration_ms"), r.DurationMS)
		}

		results = append(results, Result{
			Key:      r.Key,
			Status:   Status(r.Status),
			Duration: time.Duration(r.DurationMS) * time.Millisecond,
			Message:  r.Message,
		})
	}

	if len(p) > 0 {
		return nil, &ProtocolError{Reasons: p, Excerpt: excerpt(doc)}
	}
	return results, nil
}

// documentAfterMarker returns everything following the last line that is
// nothing but the marker.
//
// The last one, because the tester writes its document as its final act. A
// test that logs a response body can echo a marker the player's app sent
// back, but that necessarily came earlier.
func documentAfterMarker(stdout string) (string, bool) {
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == Marker {
			return strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", false
}

// decodeReason says what kind of unreadable the document is, since "invalid
// JSON" and "a field this schema does not define" call for different fixes.
func decodeReason(err error) string {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
	)

	switch {
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "the document is not valid JSON: " + err.Error()
	case errors.As(err, &typeErr):
		return fmt.Sprintf("%q has the wrong type: %v", typeErr.Field, err)
	default:
		return "the document does not match this schema: " + err.Error()
	}
}

// problems accumulates reasons a document could not be read.
type problems []string

func (p *problems) add(format string, args ...any) {
	*p = append(*p, fmt.Sprintf(format, args...))
}
