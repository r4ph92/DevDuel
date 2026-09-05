// Package tester reads what a challenge's hidden tests reported.
//
// A tester writes its results to stdout as a marker line followed by one JSON
// document:
//
//	##DEVDUEL_RESULTS##
//	{"schema":"devduel.results/1","results":[{"key":"create-todo","status":"pass","duration_ms":12,"message":""}]}
//
// The marker exists because a test runner's stdout is full of other things.
// Everything before the last marker is console noise as far as this package
// is concerned, which is also what stops a player from spoofing results: the
// tester prints its document as its final act, so anything the player's app
// echoed back through a test necessarily came earlier.
//
// The other half of the contract is that a judge run always produces one
// result per declared requirement. A tester that crashed, timed out or said
// nothing does not produce a shorter list — it produces a list of errors.
// Nothing here can silently drop a requirement, because a dropped requirement
// would score the same as one that was never broken.
package tester

import "time"

// Marker introduces the result document. It must stand alone on its line.
const Marker = "##DEVDUEL_RESULTS##"

// Schema is the document format this package reads.
const Schema = "devduel.results/1"

// Status is what happened to one requirement.
type Status string

const (
	// StatusPass means the requirement is satisfied.
	StatusPass Status = "pass"
	// StatusFail means the tests for the requirement ran and did not pass.
	StatusFail Status = "fail"
	// StatusError means the requirement's outcome is unknown: the tester did
	// not report it, or did not get far enough to try.
	StatusError Status = "error"
)

// Result is the outcome of one requirement.
type Result struct {
	// Key is the requirement's key from the challenge spec.
	Key string
	// Status is the outcome. It is never empty.
	Status Status
	// Duration is how long the tester spent on this requirement, zero if it
	// did not say.
	Duration time.Duration
	// Message is what to show the player: an assertion failure, or why the
	// outcome is unknown. It is empty for a pass.
	Message string
}

// Run is one tester container's execution.
type Run struct {
	// Stdout is everything the tester printed, which may have been truncated
	// on the way here. The result document survives that: it is the last
	// thing written, and truncation keeps the end.
	Stdout string
	// ExitCode is the tester's exit status. A non-zero status is not itself a
	// problem — a test runner exits non-zero whenever tests fail, which is
	// exactly what a broken requirement looks like.
	ExitCode int
	// Err is why the run did not finish, and nil when it did: a timeout, a
	// container that never started, a crash the runtime noticed.
	Err error
}
