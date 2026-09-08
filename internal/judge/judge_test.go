package judge_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/judge"
	"github.com/r4ph92/DevDuel/internal/tester"
)

const jobID = "job-7"

var names = struct{ network, runner, tester string }{
	network: "devduel-job-7",
	runner:  "devduel-job-7-runner",
	tester:  "devduel-job-7-tester",
}

func spec(t *testing.T) *challenge.Spec {
	t.Helper()

	// Loaded from a directory rather than parsed from bytes: the judge copies
	// the challenge's tests out of TestsDir(), so the spec has to know where
	// it lives.
	loaded, err := challenge.Load("testdata/todo-api")
	if err != nil {
		t.Fatalf("load fixture challenge: %v", err)
	}
	return loaded
}

// results renders a tester document reporting the given key=status pairs.
func results(pairs ...string) string {
	items := make([]string, len(pairs))
	for i, p := range pairs {
		key, status, _ := strings.Cut(p, "=")
		items[i] = `{"key":"` + key + `","status":"` + status + `","duration_ms":5,"message":""}`
	}
	return "running tests\n" + tester.Marker + "\n" +
		`{"schema":"` + tester.Schema + `","results":[` + strings.Join(items, ",") + `]}` + "\n"
}

func statuses(rs []tester.Result) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.Key + "=" + string(r.Status)
	}
	return strings.Join(parts, " ")
}

// newJob wires a fake runtime whose tester reports the given outcomes.
func newJob(t *testing.T, pairs ...string) (*judge.Judge, *fakeRuntime, judge.Job) {
	t.Helper()

	fake := newFakeRuntime()
	fake.logs[names.tester] = containerLogs(results(pairs...))

	return judge.New(fake), fake, judge.Job{
		ID:        jobID,
		Spec:      spec(t),
		Workspace: t.TempDir(),
	}
}

func runOK(t *testing.T, j *judge.Judge, job judge.Job) judge.Report {
	t.Helper()

	report, err := j.Run(t.Context(), job)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	return report
}

func TestRunJudgesTheWorkspaceAndReportsEveryRequirement(t *testing.T) {
	j, _, job := newJob(t, "list-todos=pass", "create-todo=fail")

	report := runOK(t, j, job)

	if got, want := statuses(report.Results), "list-todos=pass create-todo=fail"; got != want {
		t.Errorf("Results = %s, want %s", got, want)
	}
	if report.Elapsed <= 0 {
		t.Error("Elapsed should be measured")
	}
}

func TestRunCreatesAndRemovesAPerJobInternalNetwork(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	runOK(t, j, job)

	net, ok := fake.networks[names.network]
	if !ok {
		t.Fatalf("no network named %s was created:\n%s", names.network, fake.eventLog())
	}
	if !net.Internal {
		// Without --internal the judge network has egress, and a challenge
		// could install its way out of a reproducible run.
		t.Error("the judge network must be internal")
	}
	if fake.alive(names.network) {
		t.Errorf("the network was not removed:\n%s", fake.eventLog())
	}
}

func TestRunGivesTheRunnerThePlayerFilesAndNothingElse(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	runOK(t, j, job)

	runner, ok := fake.containers[names.runner]
	if !ok {
		t.Fatalf("no runner was created:\n%s", fake.eventLog())
	}
	if got, want := strings.Join(runner.Command, " "), "node server.js"; got != want {
		t.Errorf("runner command = %q, want %q", got, want)
	}
	if runner.Network != names.network {
		t.Errorf("runner network = %q, want %q", runner.Network, names.network)
	}
	if got, want := strings.Join(runner.Aliases, ","), judge.RunnerAlias; got != want {
		t.Errorf("runner aliases = %q, want %q", got, want)
	}

	// The one thing that must never happen.
	for _, copied := range fake.copies {
		if strings.Contains(copied, names.runner) && strings.Contains(copied, job.Spec.TestsDir()) {
			t.Errorf("hidden tests were copied into the runner: %s", copied)
		}
	}
}

func TestRunCopiesTheHiddenTestsOnlyIntoTheTester(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	runOK(t, j, job)

	var intoTester []string
	for _, copied := range fake.copies {
		if strings.Contains(copied, names.tester) {
			intoTester = append(intoTester, copied)
		}
	}

	if len(intoTester) != 1 {
		t.Fatalf("expected exactly one copy into the tester, got %v", intoTester)
	}
	if !strings.HasPrefix(intoTester[0], job.Spec.TestsDir()) {
		t.Errorf("the tester should receive the challenge's tests, got %q", intoTester[0])
	}
	if !strings.HasSuffix(intoTester[0], names.tester+":"+judge.TestsDir) {
		t.Errorf("the tests should land in %s, got %q", judge.TestsDir, intoTester[0])
	}
}

func TestRunTellsTheTesterWhereTheRunnerIs(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	runOK(t, j, job)

	env := fake.containers[names.tester].Env
	// By alias and port only: the tester is given no container name, no
	// address, and no way to reach anything else.
	if got, want := env[judge.EnvTarget], "http://runner:3000"; got != want {
		t.Errorf("%s = %q, want %q", judge.EnvTarget, got, want)
	}
	if got, want := env[judge.EnvHealthURL], "http://runner:3000/health"; got != want {
		t.Errorf("%s = %q, want %q", judge.EnvHealthURL, got, want)
	}
}

func TestRunNeverPublishesAPort(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	runOK(t, j, job)

	// Nothing off the network reaches either container: the tester polls the
	// runner's health endpoint itself.
	for name, c := range fake.containers {
		if c.Network != names.network {
			t.Errorf("%s is on network %q, want the job's own", name, c.Network)
		}
	}
}

func TestRunStartsTheRunnerBeforeTheTester(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	runOK(t, j, job)

	log := fake.eventLog()
	runnerAt := strings.Index(log, "start "+names.runner)
	testerAt := strings.Index(log, "start "+names.tester)

	switch {
	case runnerAt < 0 || testerAt < 0:
		t.Fatalf("both containers should be started:\n%s", log)
	case runnerAt > testerAt:
		t.Errorf("the runner must be up before the tester starts polling it:\n%s", log)
	}
}

func TestRunCollectsBothContainersLogs(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")
	fake.logs[names.runner] = containerLogs("listening on 3000\n")

	report := runOK(t, j, job)

	if !strings.Contains(report.Runner.Stdout, "listening on 3000") {
		t.Errorf("Runner logs = %q", report.Runner.Stdout)
	}
	if !strings.Contains(report.Tester.Stdout, tester.Marker) {
		t.Errorf("Tester logs = %q", report.Tester.Stdout)
	}
}

// assertNothingLeftBehind is the invariant that has to hold on every path.
func assertNothingLeftBehind(t *testing.T, fake *fakeRuntime) {
	t.Helper()

	for _, name := range []string{names.runner, names.tester, names.network} {
		if fake.alive(name) {
			t.Errorf("%s survived the job:\n%s", name, fake.eventLog())
		}
	}
}

func TestRunRemovesEverythingWhateverFails(t *testing.T) {
	steps := []string{
		"network.create " + names.network,
		"create " + names.runner,
		"cp " + names.runner + " /app",
		"start " + names.runner,
		"create " + names.tester,
		"cp " + names.tester + " " + judge.TestsDir,
		"start " + names.tester,
		"logs " + names.tester,
	}

	// "wait" is deliberately absent: a tester that never finished is a result
	// of zero, not a failure to judge. TestRunTreatsATesterTimeoutAsAResult
	// covers that path, teardown included.

	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")
			fake.failOn = step

			if _, err := j.Run(t.Context(), job); err == nil {
				t.Fatalf("Run: expected %q to fail the job", step)
			}
			assertNothingLeftBehind(t, fake)
		})
	}
}

func TestRunRemovesEverythingWhenTheCallerGivesUp(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Teardown must not inherit the dead context, or a cancelled job would
	// leak everything it created.
	_, _ = j.Run(ctx, job)

	assertNothingLeftBehind(t, fake)
}

func TestRunLeavesAnotherRunsResourcesAlone(t *testing.T) {
	// A job id that is already in use belongs to a run that is still going.
	// Creating its network fails, and teardown must not then remove the
	// containers that run is using.
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")
	fake.existing(names.network, names.runner, names.tester)
	fake.failOn = "network.create " + names.network

	if _, err := j.Run(t.Context(), job); err == nil {
		t.Fatal("Run: expected a job whose network already exists to fail")
	}

	for _, name := range []string{names.network, names.runner, names.tester} {
		if !fake.alive(name) {
			t.Errorf("%s belongs to a live run and must survive:\n%s", name, fake.eventLog())
		}
	}
}

func TestRunRemovesOnlyWhatItCreated(t *testing.T) {
	// The network is this run's, so it goes. The containers were never
	// created, so there is nothing to remove and nothing to report.
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")
	fake.failOn = "create " + names.runner

	if _, err := j.Run(t.Context(), job); err == nil {
		t.Fatal("Run: expected a failed runner create to fail the job")
	}

	if fake.alive(names.network) {
		t.Errorf("the network this run created should be gone:\n%s", fake.eventLog())
	}
	for _, event := range []string{"remove " + names.runner, "remove " + names.tester} {
		if fake.happened(event) {
			t.Errorf("%q was never created, so it must not be removed:\n%s", event, fake.eventLog())
		}
	}
}

func TestRunReportsWhatItCouldNotRemove(t *testing.T) {
	busy := errors.New("device or resource busy")

	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")
	fake.removeErr = busy

	report, err := j.Run(t.Context(), job)

	var teardownErr *judge.TeardownError
	if !errors.As(err, &teardownErr) {
		t.Fatalf("error is %T (%v), want *judge.TeardownError", err, err)
	}
	if len(teardownErr.Errs) != 3 {
		t.Errorf("TeardownError carries %d failures, want 3", len(teardownErr.Errs))
	}
	if !errors.Is(err, busy) {
		t.Error("the underlying cause should still be reachable through the error chain")
	}

	// Whoever has to clean up by hand needs to know what to look for.
	msg := err.Error()
	for _, want := range []string{names.runner, names.tester, names.network, "device or resource busy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should name %q, got:\n%s", want, msg)
		}
	}
	// A leak is an operational problem; the judging was still sound.
	if got, want := statuses(report.Results), "list-todos=pass create-todo=pass"; got != want {
		t.Errorf("Results = %s, want %s — a leak must not discard results", got, want)
	}
}

func TestRunTreatsATesterTimeoutAsAResult(t *testing.T) {
	j, fake, job := newJob(t)
	fake.waitErr = context.DeadlineExceeded
	fake.exitCode = -1

	report := runOK(t, j, job)

	if got, want := statuses(report.Results), "list-todos=error create-todo=error"; got != want {
		t.Errorf("Results = %s, want %s", got, want)
	}
	if msg := report.Results[0].Message; !strings.Contains(msg, "deadline") {
		t.Errorf("the message should say the tester ran out of time, got %q", msg)
	}
	assertNothingLeftBehind(t, fake)
}

func TestRunTreatsAnAppThatNeverStartedAsAResult(t *testing.T) {
	// The runner crashed, so the tester's health poll never succeeded and it
	// reported nothing. That is a score of zero, not a broken job.
	j, fake, job := newJob(t)
	fake.logs[names.tester] = containerLogs("waiting for http://runner:3000/health\ntimed out\n")
	fake.logs[names.runner] = containerLogs("SyntaxError: unexpected token\n")
	fake.exitCode = 1

	report := runOK(t, j, job)

	if got, want := statuses(report.Results), "list-todos=error create-todo=error"; got != want {
		t.Errorf("Results = %s, want %s", got, want)
	}
	if !strings.Contains(report.Runner.Stdout, "SyntaxError") {
		t.Error("the runner's output is the only place the reason exists; it must reach the report")
	}
}

func TestRunStillReadsLogsAfterTheJobDeadlinePasses(t *testing.T) {
	// A job deadline shorter than the challenge's tester timeout cuts the
	// wait short, leaving the job's context dead. The output explaining what
	// happened is read after that, so it cannot depend on that context.
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")
	fake.waitForContext = true

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	report, err := j.Run(ctx, job)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}

	if !strings.Contains(report.Tester.Stdout, tester.Marker) {
		t.Errorf("the tester output should survive the deadline that cut it off, got %q", report.Tester.Stdout)
	}
	if got, want := statuses(report.Results), "list-todos=error create-todo=error"; got != want {
		t.Errorf("Results = %s, want %s", got, want)
	}
	assertNothingLeftBehind(t, fake)
}

func TestRunFailsOnATesterThatEmittedNonsense(t *testing.T) {
	j, fake, job := newJob(t)
	fake.logs[names.tester] = containerLogs(tester.Marker + "\n<html>not results</html>\n")

	_, err := j.Run(t.Context(), job)

	var protoErr *tester.ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("error is %T (%v), want *tester.ProtocolError", err, err)
	}
	assertNothingLeftBehind(t, fake)
}

func TestRunBoundsTheTesterByTheChallengeTimeout(t *testing.T) {
	j, fake, job := newJob(t, "list-todos=pass", "create-todo=pass")

	runOK(t, j, job)

	if fake.waitDeadline.IsZero() {
		t.Fatal("WaitContainer was given no deadline; a hung tester would hold the job open")
	}
	if got := time.Until(fake.waitDeadline); got > job.Spec.Tester.Timeout+time.Second {
		t.Errorf("wait deadline is %v away, want about the challenge's %v", got, job.Spec.Tester.Timeout)
	}
}

func TestRunRejectsAJobItCannotName(t *testing.T) {
	cases := []struct {
		name string
		job  judge.Job
	}{
		{"no id", judge.Job{Spec: spec(t), Workspace: t.TempDir()}},
		{"id that is not a slug", judge.Job{ID: "Job 7!", Spec: spec(t), Workspace: t.TempDir()}},
		{"no spec", judge.Job{ID: jobID, Workspace: t.TempDir()}},
		{"no workspace", judge.Job{ID: jobID, Spec: spec(t)}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeRuntime()

			if _, err := judge.New(fake).Run(t.Context(), c.job); err == nil {
				t.Fatal("Run: expected an error, got nil")
			}
			if len(fake.events) != 0 {
				t.Errorf("a job that cannot be named must create nothing, got:\n%s", fake.eventLog())
			}
		})
	}
}
