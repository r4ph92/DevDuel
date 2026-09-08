// Package judge runs one challenge against one workspace and reports which
// requirements pass.
//
// A job is two containers on a throwaway network:
//
//	runner (player code)  <-- HTTP --  tester (hidden tests)
//	     one per-job internal network, no egress
//
// The split is the whole security model. Player code and hidden tests never
// share a process or a container, so the tests cannot be read, patched or
// tricked by anything the player writes — the only thing the player controls
// is the HTTP responses the tester sees.
//
// The network is created --internal, which means no egress. That is why a
// challenge image has to ship with its dependencies already baked in:
// installing anything at judge time cannot work, by design.
//
// The tester polls the runner's health endpoint itself rather than the host
// doing it, so nothing outside the network ever needs to reach either
// container. No ports are published.
//
// Everything a job creates is named after the job and removed when the job
// ends, on every path including the ones that error.
package judge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/container"
	"github.com/r4ph92/DevDuel/internal/tester"
)

// RunnerAlias is the only name the tester knows the runner by. Nothing else
// on the network answers to it, and nothing off the network can reach it.
const RunnerAlias = "runner"

// TestsDir is where the hidden tests are copied inside the tester container,
// and the directory the tester command runs from.
const TestsDir = "/tests"

// Environment the tester is given so it can find the runner without knowing
// anything about how the job was set up.
const (
	// EnvTarget is the base URL of the player's app.
	EnvTarget = "DEVDUEL_TARGET"
	// EnvHealthURL is the URL the tester polls until the app answers.
	EnvHealthURL = "DEVDUEL_HEALTH_URL"
)

// teardownTimeout bounds the removal of a job's containers and network.
const teardownTimeout = 30 * time.Second

// logTimeout bounds reading a container's output.
const logTimeout = 15 * time.Second

// Judge runs judge jobs against a container runtime.
type Judge struct {
	runtime container.Runtime
}

// New returns a Judge that runs its containers through rt.
func New(rt container.Runtime) *Judge { return &Judge{runtime: rt} }

// Job is one challenge to judge against one workspace.
type Job struct {
	// ID distinguishes this job's containers and network from every other
	// job's. It must be unique among jobs running at the same time.
	ID string
	// Spec is the challenge being judged.
	Spec *challenge.Spec
	// Workspace is a local directory whose contents are copied into the
	// runner. It holds the player's files and nothing else.
	Workspace string
}

// Report is everything one judge run produced.
type Report struct {
	// Results has one entry per declared requirement, in spec order, however
	// the run went.
	Results []tester.Result
	// Runner is the player's app output, which is where the answer lives when
	// every requirement errored because the app never came up.
	Runner container.Logs
	// Tester is the hidden tests' output, results document and all.
	Tester container.Logs
	// TesterExit is the tester's exit status, or -1 if it never finished.
	TesterExit int
	// Elapsed is how long the pair took, from network creation to results.
	Elapsed time.Duration
}

// Run judges one job.
//
// It returns a Report whenever the tester ran at all, even when every
// requirement errored: a run where the app never started is a real result,
// not a failure to judge. It returns an error when the job could not be set
// up, or when the tester's output could not be read.
//
// A *[TeardownError] means the results are sound but something was left
// behind; callers should record the leak rather than discard the Report.
func (j *Judge) Run(ctx context.Context, job Job) (report Report, err error) {
	if err := job.validate(); err != nil {
		return Report{}, err
	}

	names := namesFor(job.ID)
	started := time.Now()

	// Registered before anything exists, so it runs however the job ends,
	// including a failure to create the very first thing. It removes only
	// what this call actually created: a job id that is somehow already in
	// use belongs to a run that is still going, and tearing its containers
	// down would kill it.
	var created owned
	defer func() {
		if teardownErr := j.teardown(ctx, names, created); teardownErr != nil {
			err = errors.Join(err, teardownErr)
		}
	}()

	// The network is internal: no egress, which is why a challenge image has
	// to carry its dependencies.
	if err := j.runtime.CreateNetwork(ctx, container.NetworkSpec{
		Name:     names.network,
		Internal: true,
		Labels:   names.labels,
	}); err != nil {
		return Report{}, fmt.Errorf("create judge network: %w", err)
	}
	created.network = true

	// The runner holds the player's code. It answers to one alias on one
	// network and publishes nothing.
	if err := j.startRunner(ctx, job, names, &created); err != nil {
		return Report{}, err
	}

	// The tester holds the hidden tests. It reaches the runner by alias and
	// waits for the health endpoint itself, so the host never has to.
	if err := j.startTester(ctx, job, names, &created); err != nil {
		return Report{}, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, job.Spec.Tester.Timeout)
	defer cancel()
	exitCode, waitErr := j.runtime.WaitContainer(waitCtx, names.tester)

	// Logs are read on a context of their own. A run that timed out is
	// exactly the run whose output matters most, and by then the job's
	// context is already dead.
	logCtx, cancelLogs := context.WithTimeout(context.WithoutCancel(ctx), logTimeout)
	defer cancelLogs()

	testerLogs, err := j.runtime.Logs(logCtx, names.tester)
	if err != nil {
		return Report{}, fmt.Errorf("read tester output: %w", err)
	}
	// The runner's output is diagnostic, not load-bearing: if it cannot be
	// read, the results still stand.
	runnerLogs, _ := j.runtime.Logs(logCtx, names.runner)

	results, err := tester.Collect(job.Spec.Requirements, tester.Run{
		Stdout:   testerLogs.Stdout,
		ExitCode: exitCode,
		Err:      waitErr,
	})
	if err != nil {
		return Report{}, err
	}

	return Report{
		Results:    results,
		Runner:     runnerLogs,
		Tester:     testerLogs,
		TesterExit: exitCode,
		Elapsed:    time.Since(started),
	}, nil
}

func (j *Judge) startRunner(ctx context.Context, job Job, names names, created *owned) error {
	spec := job.Spec

	if _, err := j.runtime.CreateContainer(ctx, container.Spec{
		Name:    names.runner,
		Image:   spec.Image.Tag,
		Command: spec.App.Command,
		Workdir: spec.App.Workdir,
		Network: names.network,
		Aliases: []string{RunnerAlias},
		Env:     map[string]string{"PORT": strconv.Itoa(spec.App.Port)},
		Labels:  names.labels,
	}); err != nil {
		return fmt.Errorf("create runner: %w", err)
	}
	created.runner = true

	// The player's workspace, and only that. The hidden tests never touch
	// this container.
	if err := j.runtime.CopyTo(ctx, names.runner, contentsOf(job.Workspace), spec.App.Workdir); err != nil {
		return fmt.Errorf("copy workspace into runner: %w", err)
	}
	if err := j.runtime.StartContainer(ctx, names.runner); err != nil {
		return fmt.Errorf("start runner: %w", err)
	}
	return nil
}

func (j *Judge) startTester(ctx context.Context, job Job, names names, created *owned) error {
	spec := job.Spec
	target := fmt.Sprintf("http://%s:%d", RunnerAlias, spec.App.Port)

	if _, err := j.runtime.CreateContainer(ctx, container.Spec{
		Name:    names.tester,
		Image:   spec.Image.Tag,
		Command: spec.Tester.Command,
		Workdir: TestsDir,
		Network: names.network,
		Env: map[string]string{
			EnvTarget:    target,
			EnvHealthURL: target + spec.App.HealthPath,
		},
		Labels: names.labels,
	}); err != nil {
		return fmt.Errorf("create tester: %w", err)
	}
	created.tester = true

	if err := j.runtime.CopyTo(ctx, names.tester, contentsOf(spec.TestsDir()), TestsDir); err != nil {
		return fmt.Errorf("copy hidden tests into tester: %w", err)
	}
	if err := j.runtime.StartContainer(ctx, names.tester); err != nil {
		return fmt.Errorf("start tester: %w", err)
	}
	return nil
}

// owned marks what one Run actually brought into existence.
//
// Removal by name alone would be wrong: a create that failed because the name
// was taken means the object belongs to somebody else, and removing it would
// end a run that is still going.
type owned struct {
	network bool
	runner  bool
	tester  bool
}

// teardown removes what this run created, and only that.
//
// It runs on a context detached from the job's, because the usual reason to
// be tearing down is that the job's context is already dead, and a cancelled
// job that leaves containers running is worse than a slow one.
func (j *Judge) teardown(ctx context.Context, names names, created owned) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
	defer cancel()

	var errs []error

	// Containers before the network: a network with attached endpoints
	// cannot be removed.
	containers := map[string]bool{names.runner: created.runner, names.tester: created.tester}
	for _, name := range []string{names.runner, names.tester} {
		if !containers[name] {
			continue
		}
		if err := j.runtime.RemoveContainer(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("remove container %s: %w", name, err))
		}
	}
	if created.network {
		if err := j.runtime.RemoveNetwork(ctx, names.network); err != nil {
			errs = append(errs, fmt.Errorf("remove network %s: %w", names.network, err))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return &TeardownError{Errs: errs}
}
