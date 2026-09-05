package judge

import (
	"fmt"
	"strings"
)

// TeardownError reports what a job left behind.
//
// It never invalidates a Report produced before it: the judging was sound and
// something merely failed to be cleaned up. A caller should treat it as an
// operational problem to record, not as a reason to discard results.
type TeardownError struct {
	Errs []error
}

func (e *TeardownError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "judge teardown left resources behind (%d)", len(e.Errs))
	for _, err := range e.Errs {
		fmt.Fprintf(&b, "\n  %v", err)
	}
	return b.String()
}

func (e *TeardownError) Unwrap() []error { return e.Errs }
