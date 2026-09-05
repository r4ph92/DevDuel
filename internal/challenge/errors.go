package challenge

import (
	"fmt"
	"sort"
	"strings"
)

// FieldError names one thing wrong with a spec and where it is.
//
// Field is a path into challenge.yaml — "app.port", "requirements[2].weight" —
// or, for a missing part of the challenge directory, the path that is absent.
type FieldError struct {
	Field string
	Msg   string
}

func (e FieldError) Error() string { return e.Field + ": " + e.Msg }

// InvalidSpecError reports every problem found in one challenge, so an author
// fixing a spec sees the whole list instead of one error per run.
type InvalidSpecError struct {
	// Path is the challenge.yaml the problems came from, when it was read
	// from disk.
	Path   string
	Fields []FieldError
}

func (e *InvalidSpecError) Error() string {
	where := "challenge spec"
	if e.Path != "" {
		where = e.Path
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", where, plural(len(e.Fields), "problem"))
	for _, f := range e.Fields {
		fmt.Fprintf(&b, "\n  %s", f.Error())
	}
	return b.String()
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// problems accumulates field errors during validation.
type problems struct {
	fields []FieldError
}

func (p *problems) add(field, format string, args ...any) {
	p.fields = append(p.fields, FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
}

// err returns an *InvalidSpecError, or nil when nothing was wrong. Fields are
// sorted so the same bad spec always reports in the same order.
func (p *problems) err(path string) error {
	if len(p.fields) == 0 {
		return nil
	}
	sort.SliceStable(p.fields, func(i, j int) bool { return p.fields[i].Field < p.fields[j].Field })
	return &InvalidSpecError{Path: path, Fields: p.fields}
}

// DuplicateKeyError reports two challenges claiming the same (id, version).
// Challenges are immutable, so a change must come with a new version.
type DuplicateKeyError struct {
	Key   Key
	Paths [2]string
}

func (e *DuplicateKeyError) Error() string {
	return fmt.Sprintf("duplicate challenge %s: %s and %s", e.Key, e.Paths[0], e.Paths[1])
}

// has reports whether field already has a problem, so a later check does not
// pile a second complaint onto a value it already knows is bad.
func (p *problems) has(field string) bool {
	for _, f := range p.fields {
		if f.Field == field {
			return true
		}
	}
	return false
}
