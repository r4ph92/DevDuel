package verify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/container"
	"github.com/r4ph92/DevDuel/internal/judge"
	"github.com/r4ph92/DevDuel/internal/tester"
	"github.com/r4ph92/DevDuel/internal/verify"
)

// fakeBuilder records the image it was asked to build.
type fakeBuilder struct {
	spec container.ImageSpec
	err  error
}

func (f *fakeBuilder) BuildImage(_ context.Context, spec container.ImageSpec) error {
	f.spec = spec
	return f.err
}

// fakeRunner answers each job from a scripted set of results, keyed by the
// directory being judged. Verification is about comparing runs, so the runs
// themselves are exactly what a test wants to control.
type fakeRunner struct {
	// byDir maps a workspace directory to the statuses it should produce, as
	// "key=status" pairs. The solution directory can be given two answers,
	// used in order, to model a challenge that is not reproducible.
	byDir map[string][]string
	calls []judge.Job
	err   error
}

func (f *fakeRunner) Run(_ context.Context, job judge.Job) (judge.Report, error) {
	f.calls = append(f.calls, job)
	if f.err != nil {
		return judge.Report{}, f.err
	}

	answers := f.byDir[job.Workspace]
	answer := answers[0]
	if len(answers) > 1 {
		f.byDir[job.Workspace] = answers[1:]
	}
	return judge.Report{Results: results(answer)}, nil
}

// results parses "key=status key=status" into tester results.
func results(spec string) []tester.Result {
	if spec == "" {
		return nil
	}

	var out []tester.Result
	for _, pair := range strings.Fields(spec) {
		key, status, _ := strings.Cut(pair, "=")
		out = append(out, tester.Result{Key: key, Status: tester.Status(status)})
	}
	return out
}

func spec(t *testing.T) *challenge.Spec {
	t.Helper()

	loaded, err := challenge.Load("testdata/todo-api")
	if err != nil {
		t.Fatalf("load fixture challenge: %v", err)
	}
	return loaded
}

// verifier wires a Verifier whose runs are scripted per phase.
func verifier(t *testing.T, workspace, solution, repeat string) (*verify.Verifier, *fakeBuilder, *fakeRunner) {
	t.Helper()

	s := spec(t)
	builder := &fakeBuilder{}
	runner := &fakeRunner{byDir: map[string][]string{
		s.WorkspaceDir(): {workspace},
		s.SolutionDir():  {solution, repeat},
	}}
	return verify.New(builder, runner), builder, runner
}

// The fixture declares list-todos broken and create-todo whole.
const (
	asDeclared = "list-todos=fail create-todo=pass"
	allPass    = "list-todos=pass create-todo=pass"
)

func problems(report verify.Report) string {
	lines := make([]string, len(report.Problems))
	for i, p := range report.Problems {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

func TestChallengeIsValidWhenItBehavesAsDeclared(t *testing.T) {
	v, builder, runner := verifier(t, asDeclared, allPass, allPass)

	report, err := v.Challenge(t.Context(), spec(t))
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}

	if !report.Valid() {
		t.Errorf("challenge should be valid, got problems:\n%s", problems(report))
	}
	if got, want := report.Challenge.String(), "todo-api@v1"; got != want {
		t.Errorf("Challenge = %q, want %q", got, want)
	}
	if got, want := builder.spec.Tag, spec(t).Image.Tag; got != want {
		t.Errorf("built %q, want the challenge's %q", got, want)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("judged %d times, want three: workspace, solution, solution again", len(runner.calls))
	}
}

func TestChallengeJudgesTheWorkspaceThenTheSolutionTwice(t *testing.T) {
	s := spec(t)
	v, _, runner := verifier(t, asDeclared, allPass, allPass)

	if _, err := v.Challenge(t.Context(), s); err != nil {
		t.Fatalf("Challenge: %v", err)
	}

	want := []string{s.WorkspaceDir(), s.SolutionDir(), s.SolutionDir()}
	for i, job := range runner.calls {
		if job.Workspace != want[i] {
			t.Errorf("run %d judged %q, want %q", i, job.Workspace, want[i])
		}
	}

	// Concurrent verifications must not collide on container names.
	ids := map[string]bool{}
	for _, job := range runner.calls {
		if ids[job.ID] {
			t.Errorf("job id %q was reused; two runs would fight over the same containers", job.ID)
		}
		ids[job.ID] = true
	}
}

func TestChallengeRejectsAWorkspaceThatDoesNotFailAsDeclared(t *testing.T) {
	cases := []struct {
		name      string
		workspace string
		wantKey   string
		wantText  string
	}{
		{
			"a broken requirement that already passes",
			"list-todos=pass create-todo=pass",
			"list-todos",
			"fail, as declared broken",
		},
		{
			"a whole requirement that fails anyway",
			"list-todos=fail create-todo=fail",
			"create-todo",
			"pass, as not declared broken",
		},
		{
			"a requirement the tester never reported",
			"list-todos=fail",
			"create-todo",
			"no result at all",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, _, _ := verifier(t, c.workspace, allPass, allPass)

			report, err := v.Challenge(t.Context(), spec(t))
			if err != nil {
				t.Fatalf("Challenge: %v", err)
			}
			if report.Valid() {
				t.Fatal("challenge should not be valid")
			}

			got := problems(report)
			if !strings.Contains(got, c.wantKey) {
				t.Errorf("problems should name %q, got:\n%s", c.wantKey, got)
			}
			if !strings.Contains(got, c.wantText) {
				t.Errorf("problems should explain %q, got:\n%s", c.wantText, got)
			}
			if !strings.Contains(got, string(verify.PhaseWorkspace)) {
				t.Errorf("problems should name the phase, got:\n%s", got)
			}
		})
	}
}

func TestChallengeRejectsASolutionThatDoesNotPass(t *testing.T) {
	v, _, _ := verifier(t, asDeclared, "list-todos=pass create-todo=fail", allPass)

	report, err := v.Challenge(t.Context(), spec(t))
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}

	if report.Valid() {
		t.Fatal("a challenge its own solution cannot solve is not valid")
	}
	got := problems(report)
	if !strings.Contains(got, "solution: create-todo") {
		t.Errorf("problems should name the phase and the requirement, got:\n%s", got)
	}
}

func TestChallengeRejectsResultsThatDoNotReproduce(t *testing.T) {
	// Same solution, judged twice, two different answers. A challenge like
	// this would rank players on noise.
	v, _, _ := verifier(t, asDeclared, allPass, "list-todos=pass create-todo=fail")

	report, err := v.Challenge(t.Context(), spec(t))
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}

	if report.Valid() {
		t.Fatal("a challenge whose results change between runs is not valid")
	}
	got := problems(report)
	if !strings.Contains(got, string(verify.PhaseRepeat)) || !strings.Contains(got, "create-todo") {
		t.Errorf("problems should name the repeat phase and the requirement, got:\n%s", got)
	}
}

func TestChallengeReportsEveryDivergenceAtOnce(t *testing.T) {
	v, _, _ := verifier(t, allPass, "list-todos=fail create-todo=fail", allPass)

	report, err := v.Challenge(t.Context(), spec(t))
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}

	// One workspace divergence, two solution failures, two repeat mismatches.
	if len(report.Problems) < 4 {
		t.Errorf("an author fixing a challenge should see every problem, got only:\n%s", problems(report))
	}
}

func TestChallengeKeepsEveryRunForInspection(t *testing.T) {
	v, _, _ := verifier(t, asDeclared, allPass, allPass)

	report, err := v.Challenge(t.Context(), spec(t))
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}

	for _, phase := range []verify.Phase{verify.PhaseWorkspace, verify.PhaseSolution, verify.PhaseRepeat} {
		if len(report.Runs[phase]) != 2 {
			t.Errorf("Runs[%s] = %v, want both requirements", phase, report.Runs[phase])
		}
	}
}

func TestChallengeFailsWhenTheImageWillNotBuild(t *testing.T) {
	s := spec(t)
	builder := &fakeBuilder{err: errors.New("npm ERR! 404 Not Found")}
	runner := &fakeRunner{byDir: map[string][]string{}}

	_, err := verify.New(builder, runner).Challenge(t.Context(), s)

	if err == nil {
		t.Fatal("Challenge: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "npm ERR!") {
		t.Errorf("error should carry why the build failed, got: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Error("nothing should be judged when there is no image to judge with")
	}
}

func TestChallengeFailsWhenAJudgeRunCannotBeCarriedOut(t *testing.T) {
	s := spec(t)
	runner := &fakeRunner{byDir: map[string][]string{}, err: errors.New("docker daemon is not running")}

	_, err := verify.New(&fakeBuilder{}, runner).Challenge(t.Context(), s)

	if err == nil {
		t.Fatal("Challenge: expected an error, got nil")
	}
	// An unusable judge is not a verdict on the challenge.
	if !strings.Contains(err.Error(), "daemon is not running") {
		t.Errorf("error should say what went wrong, got: %v", err)
	}
}

func TestChallengePreservesJudgeFailureWhenTeardownAlsoFails(t *testing.T) {
	judgeErr := errors.New("start runner: daemon disconnected")
	teardownErr := &judge.TeardownError{Errs: []error{errors.New("remove network: daemon disconnected")}}
	runner := &fakeRunner{err: errors.Join(judgeErr, teardownErr)}

	_, err := verify.New(&fakeBuilder{}, runner).Challenge(t.Context(), spec(t))

	if !errors.Is(err, judgeErr) || !errors.Is(err, teardownErr) {
		t.Fatalf("Challenge: got %v, want both the judging and cleanup errors", err)
	}
	if len(runner.calls) != 1 {
		t.Errorf("judged %d times, want verification to stop after the failed run", len(runner.calls))
	}
}

func TestChallengeStillReportsWhenAJudgeRunLeaksResources(t *testing.T) {
	// A leaked container is the operator's problem. The results were sound,
	// so the verdict stands rather than being thrown away.
	s := spec(t)
	runner := &leakyRunner{fakeRunner{byDir: map[string][]string{
		s.WorkspaceDir(): {asDeclared},
		s.SolutionDir():  {allPass, allPass},
	}}}

	report, err := verify.New(&fakeBuilder{}, runner).Challenge(t.Context(), s)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	if !report.Valid() {
		t.Errorf("challenge should still be judged valid, got:\n%s", problems(report))
	}
}

type leakyRunner struct{ fakeRunner }

func (l *leakyRunner) Run(ctx context.Context, job judge.Job) (judge.Report, error) {
	report, _ := l.fakeRunner.Run(ctx, job)
	// Judge.Run joins cleanup errors even when judging itself succeeded.
	return report, errors.Join(nil, &judge.TeardownError{Errs: []error{errors.New("device or resource busy")}})
}
