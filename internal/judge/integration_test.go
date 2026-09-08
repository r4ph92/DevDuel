package judge_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/container"
	"github.com/r4ph92/DevDuel/internal/judge"
	"github.com/r4ph92/DevDuel/internal/tester"
)

// These exercise the judge against a real daemon. The fake runtime can say
// what the judge asked for; only a daemon can say what happens when a
// container refuses to start, outlives its timeout, or shares a name with
// something already running. The `docker cp` that a read-only rootfs rejects
// is the standing reminder that the difference matters.

// requireDocker skips unless a daemon answers.
func requireDocker(t *testing.T) *container.CLI {
	t.Helper()

	if testing.Short() {
		t.Skip("docker integration test skipped in -short mode")
	}

	cli := container.NewCLI(container.WithCommandTimeout(2 * time.Minute))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	if err := cli.RemoveContainer(ctx, "devduel-probe-that-does-not-exist"); err != nil {
		t.Skipf("docker not available: %v", err)
	}
	return cli
}

// uniqueJobID makes an id unique to this test, so a run cannot collide with
// anything else on the machine.
func uniqueJobID(t *testing.T) string {
	t.Helper()

	name := strings.ToLower(t.Name())
	name = strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(name)
	return fmt.Sprintf("it-%s-%d", name, time.Now().UnixNano()%1e9)
}

// fixture loads one of the lifecycle challenges in testdata.
func fixture(t *testing.T, name string) *challenge.Spec {
	t.Helper()

	spec, err := challenge.Load("testdata/" + name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return spec
}

// runFixture judges one fixture and guarantees nothing is left behind even if
// the judge itself fails to clean up.
func runFixture(t *testing.T, cli *container.CLI, name, id string) (judge.Report, error) {
	t.Helper()

	spec := fixture(t, name)
	t.Cleanup(func() { sweep(t, cli, id) })

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	return judge.New(cli).Run(ctx, judge.Job{ID: id, Spec: spec, Workspace: spec.WorkspaceDir()})
}

// sweep removes anything a job may have left, so a failing test does not
// poison the machine for the next one.
func sweep(t *testing.T, cli *container.CLI, id string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()

	base := "devduel-" + id
	_ = cli.RemoveContainer(ctx, base+"-runner")
	_ = cli.RemoveContainer(ctx, base+"-tester")
	_ = cli.RemoveNetwork(ctx, base)
}

// exists asks docker directly, rather than asking the judge whether it thinks
// it cleaned up. Teardown is the one thing that has to be checked from
// outside.
func exists(t *testing.T, kind, name string) bool {
	t.Helper()

	args := []string{"inspect", name}
	if kind == "network" {
		args = []string{"network", "inspect", name}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Run() == nil
}

func assertNothingLeft(t *testing.T, id string) {
	t.Helper()

	base := "devduel-" + id
	for _, what := range []struct{ kind, name string }{
		{"container", base + "-runner"},
		{"container", base + "-tester"},
		{"network", base},
	} {
		if exists(t, what.kind, what.name) {
			t.Errorf("%s %s survived the job", what.kind, what.name)
		}
	}
}

func statusOf(results []tester.Result) string {
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Key + "=" + string(r.Status)
	}
	return strings.Join(parts, " ")
}

func TestRunReportsEveryRequirementWhenTheAppNeverBoots(t *testing.T) {
	cli := requireDocker(t)
	id := uniqueJobID(t)

	report, err := runFixture(t, cli, "never-boots", id)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A player whose code does not start scores zero. That is a result, not a
	// broken job.
	if got, want := statusOf(report.Results), "first=error second=error"; got != want {
		t.Errorf("Results = %s, want %s", got, want)
	}
	for _, r := range report.Results {
		if r.Message == "" {
			t.Errorf("%s: an error with no explanation is not actionable", r.Key)
		}
	}

	// The reason only exists in the runner's own output, so it has to survive
	// into the report.
	runner := report.Runner.Stdout + report.Runner.Stderr
	if !strings.Contains(runner, "cannot bind") {
		t.Errorf("the app's failure should reach the report, got %q", runner)
	}

	assertNothingLeft(t, id)
}

func TestRunDistinguishesATesterTimeoutFromATestFailure(t *testing.T) {
	cli := requireDocker(t)

	timedOutID, failedID := uniqueJobID(t)+"-a", uniqueJobID(t)+"-b"

	timedOut, err := runFixture(t, cli, "tester-hangs", timedOutID)
	if err != nil {
		t.Fatalf("Run tester-hangs: %v", err)
	}
	failed, err := runFixture(t, cli, "quick", failedID)
	if err != nil {
		t.Fatalf("Run quick: %v", err)
	}

	// The difference that matters: a tester that never finished says nothing
	// about the requirements, while one that ran and failed says a great deal.
	if got, want := statusOf(timedOut.Results), "first=error second=error"; got != want {
		t.Errorf("a tester timeout gave %s, want %s", got, want)
	}
	if got, want := statusOf(failed.Results), "first=pass second=fail"; got != want {
		t.Errorf("a tester that reported gave %s, want %s", got, want)
	}

	message := timedOut.Results[0].Message
	if !strings.Contains(message, "did not finish") || !strings.Contains(message, "deadline") {
		t.Errorf("a timeout should say the tester ran out of time, got %q", message)
	}
	if timedOut.Results[0].Status == tester.StatusFail {
		t.Error("a timeout must never read as a failed test")
	}

	assertNothingLeft(t, timedOutID)
	assertNothingLeft(t, failedID)
}

func TestConcurrentJobsDoNotCollide(t *testing.T) {
	cli := requireDocker(t)

	const jobs = 4
	ids := make([]string, jobs)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", uniqueJobID(t), i)
	}

	reports := make([]judge.Report, jobs)
	errs := make([]error, jobs)

	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], errs[i] = runFixture(t, cli, "quick", ids[i])
		}()
	}
	wg.Wait()

	for i := range jobs {
		if errs[i] != nil {
			t.Errorf("job %d: %v", i, errs[i])
			continue
		}
		if got, want := statusOf(reports[i].Results), "first=pass second=fail"; got != want {
			t.Errorf("job %d returned %s, want %s", i, got, want)
		}
		assertNothingLeft(t, ids[i])
	}
}

func TestRunLeavesALiveJobsResourcesAlone(t *testing.T) {
	// The ownership rule, against a real daemon: a job whose id is already in
	// use must fail without tearing down whatever is using it.
	cli := requireDocker(t)
	id := uniqueJobID(t)
	network := "devduel-" + id

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	// Stand in for a run that already owns this id.
	if err := cli.CreateNetwork(ctx, container.NetworkSpec{Name: network, Internal: true}); err != nil {
		t.Fatalf("create the incumbent network: %v", err)
	}
	t.Cleanup(func() { sweep(t, cli, id) })

	_, err := runFixture(t, cli, "quick", id)

	if err == nil {
		t.Fatal("Run: a job whose network already exists must fail")
	}
	var teardownErr *judge.TeardownError
	if errors.As(err, &teardownErr) {
		t.Errorf("the failure should be the collision, not teardown: %v", err)
	}
	if !exists(t, "network", network) {
		t.Error("the network belongs to a run that is still going and must survive")
	}
}
