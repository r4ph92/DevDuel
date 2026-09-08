// Package verify decides whether a challenge is fit to be played.
//
// A challenge makes two claims: that its starting workspace fails exactly the
// requirements it declares broken, and that its reference solution passes
// everything. Neither is checkable by reading the files. Both are checked here
// by building the image and judging the challenge against its own directories,
// the same way a match would.
//
// The third claim is that the result is stable. A challenge that scores
// differently on two identical runs would rank players on noise, so the
// solution is judged twice and the results must agree.
//
// This is the gate that keeps the ranked pool trustworthy, which is why it
// fails loudly and names every requirement that disagreed.
package verify

import (
	"context"
	"errors"
	"fmt"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/container"
	"github.com/r4ph92/DevDuel/internal/judge"
	"github.com/r4ph92/DevDuel/internal/tester"
)

// Builder builds the challenge's image.
type Builder interface {
	BuildImage(ctx context.Context, spec container.ImageSpec) error
}

// Runner judges one job. [judge.Judge] is the implementation.
type Runner interface {
	Run(ctx context.Context, job judge.Job) (judge.Report, error)
}

// Phase is one judging pass performed during verification.
type Phase string

const (
	// PhaseWorkspace judges the starting repository players are given.
	PhaseWorkspace Phase = "workspace"
	// PhaseSolution judges the reference solution.
	PhaseSolution Phase = "solution"
	// PhaseRepeat judges the reference solution a second time.
	PhaseRepeat Phase = "repeat"
)

// Verifier checks challenges.
type Verifier struct {
	builder Builder
	runner  Runner
	// suffix distinguishes this verifier's job names from anything else
	// running at the same time.
	suffix func() string
}

// New returns a Verifier that builds through b and judges through r.
func New(b Builder, r Runner) *Verifier {
	return &Verifier{builder: b, runner: r, suffix: randomSuffix}
}

// Problem is one way a challenge failed to be what it claims.
type Problem struct {
	// Phase is the run that disagreed.
	Phase Phase
	// Requirement is the key that diverged, empty when the whole phase did.
	Requirement string
	// Want and Got describe the disagreement in the reader's terms.
	Want string
	Got  string
}

func (p Problem) String() string {
	if p.Requirement == "" {
		return fmt.Sprintf("%s: expected %s, got %s", p.Phase, p.Want, p.Got)
	}
	return fmt.Sprintf("%s: %s: expected %s, got %s", p.Phase, p.Requirement, p.Want, p.Got)
}

// Report is what verifying one challenge found.
type Report struct {
	Challenge challenge.Key
	// Runs holds each phase's results, so a caller can show what happened
	// rather than only what was wrong.
	Runs map[Phase][]tester.Result
	// Problems is empty exactly when the challenge is valid.
	Problems []Problem
}

// Valid reports whether the challenge may be played.
func (r Report) Valid() bool { return len(r.Problems) == 0 }

// Challenge builds the challenge's image and judges it three times: once
// against the starting workspace, twice against the reference solution.
//
// It returns a Report whenever every run completed, whether or not the
// challenge turned out to be valid; an invalid challenge is an answer, not an
// error. It returns an error only when a run could not be carried out at all,
// because then there is nothing to conclude.
func (v *Verifier) Challenge(ctx context.Context, spec *challenge.Spec) (Report, error) {
	report := Report{Challenge: spec.Key(), Runs: map[Phase][]tester.Result{}}

	if err := v.builder.BuildImage(ctx, container.ImageSpec{
		Tag:        spec.Image.Tag,
		ContextDir: spec.ImageContextDir(),
		Labels:     map[string]string{"devduel.challenge": spec.ID},
	}); err != nil {
		return Report{}, fmt.Errorf("build challenge image: %w", err)
	}

	phases := []struct {
		phase Phase
		dir   string
	}{
		{PhaseWorkspace, spec.WorkspaceDir()},
		{PhaseSolution, spec.SolutionDir()},
		{PhaseRepeat, spec.SolutionDir()},
	}

	for _, p := range phases {
		results, err := v.judge(ctx, spec, p.phase, p.dir)
		if err != nil {
			return Report{}, fmt.Errorf("judge the %s: %w", p.phase, err)
		}
		report.Runs[p.phase] = results
	}

	report.Problems = inspect(spec, report.Runs)
	return report, nil
}

// judge runs one phase against one directory.
func (v *Verifier) judge(ctx context.Context, spec *challenge.Spec, phase Phase, dir string) ([]tester.Result, error) {
	report, err := v.runner.Run(ctx, judge.Job{
		ID:        fmt.Sprintf("verify-%s-%s-%s", spec.ID, phase, v.suffix()),
		Spec:      spec,
		Workspace: dir,
	})
	if err != nil {
		// A judge run that produced results and then failed to clean up is
		// still a run: the results are sound, and the leak is the operator's
		// problem rather than the challenge author's.
		// Judge.Run returns no results when judging fails and may join that
		// failure with a teardown error. Preserve both errors in that case.
		var teardownErr *judge.TeardownError
		if len(report.Results) == 0 || !errors.As(err, &teardownErr) {
			return nil, err
		}
	}
	return report.Results, nil
}

// inspect turns three runs into the list of ways the challenge is not what it
// says it is.
func inspect(spec *challenge.Spec, runs map[Phase][]tester.Result) []Problem {
	var problems []Problem

	// The starting workspace must fail exactly what is declared broken. A
	// requirement that is broken but passes means the challenge is already
	// solved; one that is whole but fails means the player is asked to fix
	// something nobody declared.
	workspace := index(runs[PhaseWorkspace])
	for _, req := range spec.Requirements {
		want := tester.StatusPass
		if req.Broken {
			want = tester.StatusFail
		}
		if got := workspace[req.Key]; got.Status != want {
			problems = append(problems, Problem{
				Phase:       PhaseWorkspace,
				Requirement: req.Key,
				Want:        declaredAs(req.Broken),
				Got:         describe(got),
			})
		}
	}

	// The reference solution must pass everything, or the challenge is not
	// solvable as written.
	solution := index(runs[PhaseSolution])
	for _, req := range spec.Requirements {
		if got := solution[req.Key]; got.Status != tester.StatusPass {
			problems = append(problems, Problem{
				Phase:       PhaseSolution,
				Requirement: req.Key,
				Want:        "pass",
				Got:         describe(got),
			})
		}
	}

	// Two identical runs must agree, or the challenge ranks players on noise.
	repeat := index(runs[PhaseRepeat])
	for _, req := range spec.Requirements {
		first, second := solution[req.Key], repeat[req.Key]
		if first.Status != second.Status {
			problems = append(problems, Problem{
				Phase:       PhaseRepeat,
				Requirement: req.Key,
				Want:        "the same result twice, " + string(first.Status),
				Got:         string(second.Status),
			})
		}
	}
	return problems
}

func index(results []tester.Result) map[string]tester.Result {
	byKey := make(map[string]tester.Result, len(results))
	for _, r := range results {
		byKey[r.Key] = r
	}
	return byKey
}

func declaredAs(broken bool) string {
	if broken {
		return "fail, as declared broken"
	}
	return "pass, as not declared broken"
}

// describe says what happened in a way that distinguishes a genuine result
// from the tester never reporting one.
func describe(r tester.Result) string {
	switch r.Status {
	case "":
		return "no result at all"
	case tester.StatusError:
		if r.Message != "" {
			return "error: " + r.Message
		}
		return "error"
	default:
		return string(r.Status)
	}
}
