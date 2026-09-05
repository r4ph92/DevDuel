package tester

import (
	"errors"
	"fmt"

	"github.com/r4ph92/DevDuel/internal/challenge"
)

// missingMessage is what a requirement the tester never mentioned carries.
const missingMessage = "the tester reported no result for this requirement"

// Collect turns one tester run into exactly one result per declared
// requirement, in the order the challenge declares them.
//
// A tester that crashed, timed out or said nothing still yields a complete
// set: every requirement comes back as [StatusError] explaining what happened.
// A missing result must never read as a pass.
//
// A document that is present but unreadable is different. That is a broken
// tester rather than a failed attempt, so it returns a *[ProtocolError] and
// the job fails loudly instead of scoring the player on nonsense.
func Collect(requirements []challenge.Requirement, run Run) ([]Result, error) {
	if run.Err != nil {
		return allErrors(requirements, "the tester did not finish: "+run.Err.Error()), nil
	}

	declared := make(map[string]bool, len(requirements))
	for _, req := range requirements {
		declared[req.Key] = true
	}

	reported, err := parse(run.Stdout, declared)
	switch {
	case errors.Is(err, ErrNoResults):
		return allErrors(requirements, fmt.Sprintf(
			"the tester exited with status %d without reporting any results", run.ExitCode)), nil
	case err != nil:
		return nil, err
	}

	byKey := make(map[string]Result, len(reported))
	for _, r := range reported {
		byKey[r.Key] = r
	}

	results := make([]Result, len(requirements))
	for i, req := range requirements {
		reported, ok := byKey[req.Key]
		if !ok {
			reported = Result{Key: req.Key, Status: StatusError, Message: missingMessage}
		}
		results[i] = reported
	}
	return results, nil
}

// allErrors is the complete set produced when the tester reported nothing at
// all. Every requirement carries the same explanation, because the same thing
// happened to all of them.
func allErrors(requirements []challenge.Requirement, message string) []Result {
	results := make([]Result, len(requirements))
	for i, req := range requirements {
		results[i] = Result{Key: req.Key, Status: StatusError, Message: message}
	}
	return results
}
