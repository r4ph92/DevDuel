package container

import (
	"fmt"
	"strings"
)

// Error is a docker command that failed.
//
// It carries the whole invocation and everything the command printed, because
// the first thing anyone does with a judge failure is run the command again by
// hand.
type Error struct {
	// Args is the full command, binary first.
	Args []string
	// ExitCode is the command's status, or -1 when it never ran.
	ExitCode int
	// Output is stdout and stderr together, capped like any other read.
	Output string
	// Err is why the command failed, when the exit code does not already say
	// it: a context error for a timeout or a cancellation, a start failure
	// when the binary could not be run, or a description of output that made
	// no sense. It is nil for a plain non-zero exit.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(strings.Join(e.Args, " "))

	switch {
	case e.Err != nil:
		fmt.Fprintf(&b, ": %v", e.Err)
	case e.ExitCode >= 0:
		fmt.Fprintf(&b, ": exit status %d", e.ExitCode)
	}
	if out := strings.TrimSpace(e.Output); out != "" {
		b.WriteString(": " + out)
	}
	return b.String()
}
func (e *Error) Unwrap() error { return e.Err }
