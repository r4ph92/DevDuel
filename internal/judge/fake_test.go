package judge_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/r4ph92/DevDuel/internal/container"
)

// containerLogs is output a fake container "printed" on stdout.
func containerLogs(stdout string) container.Logs { return container.Logs{Stdout: stdout} }

// fakeRuntime stands in for docker. It records what the judge asked for, in
// order, and can be told to fail any one call so the teardown paths can be
// driven without a daemon.
//
// The real thing is exercised end to end in the judge integration tests; what
// matters here is the orchestration: what gets created, what gets copied
// where, and what is left behind when a step fails.
type fakeRuntime struct {
	mu sync.Mutex

	// events is every call in order, rendered for comparison.
	events []string
	// containers is what each container was created with, by name.
	containers map[string]container.Spec
	// networks is what each network was created with, by name.
	networks map[string]container.NetworkSpec
	// live is what currently exists. Created objects go in, successfully
	// removed ones come out. A removal that was attempted and refused leaves
	// the object alive, which is the whole point: the invariant is that
	// nothing the job created survives it, not that the judge tried.
	live map[string]bool

	// logs is what each container "printed", by name.
	logs map[string]container.Logs
	// exitCode is what WaitContainer reports.
	exitCode int
	// waitErr is what WaitContainer fails with, if anything.
	waitErr error
	// waitForContext makes WaitContainer block until the context ends, the
	// way a real wait does when the job runs out of time.
	waitForContext bool

	// waitDeadline is the deadline WaitContainer was called with, so a test
	// can check the tester is actually bounded.
	waitDeadline time.Time

	// failOn makes the named event fail, e.g. "start devduel-7-runner".
	failOn string
	// removeErr makes every removal fail.
	removeErr error
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		containers: map[string]container.Spec{},
		networks:   map[string]container.NetworkSpec{},
		logs:       map[string]container.Logs{},
		live:       map[string]bool{},
	}
}

// record logs an event and reports whether it should fail.
//
// It honours the context, like the real runtime does. That is what lets a
// test tell whether teardown was handed a context the job had already
// cancelled — with a fake that ignored ctx, the check would pass either way.
func (f *fakeRuntime) record(ctx context.Context, event string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.events = append(f.events, event)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake runtime: %s: %w", event, err)
	}
	if f.failOn != "" && f.failOn == event {
		return fmt.Errorf("fake runtime: %s failed", event)
	}
	return nil
}

func (f *fakeRuntime) eventLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return strings.Join(f.events, "\n")
}

// happened reports whether an event was recorded.
func (f *fakeRuntime) happened(event string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, e := range f.events {
		if e == event {
			return true
		}
	}
	return false
}

func (f *fakeRuntime) CreateNetwork(ctx context.Context, spec container.NetworkSpec) error {
	if err := f.record(ctx, "network.create "+spec.Name); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[spec.Name] = spec
	f.live[spec.Name] = true
	return nil
}

func (f *fakeRuntime) RemoveNetwork(ctx context.Context, name string) error {
	if err := f.record(ctx, "network.remove "+name); err != nil {
		return err
	}
	return f.markGone(name)
}

func (f *fakeRuntime) CreateContainer(ctx context.Context, spec container.Spec) (string, error) {
	if err := f.record(ctx, "create "+spec.Name); err != nil {
		return "", err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[spec.Name] = spec
	f.live[spec.Name] = true
	return spec.Name, nil
}

func (f *fakeRuntime) StartContainer(ctx context.Context, name string) error {
	return f.record(ctx, "start "+name)
}

func (f *fakeRuntime) WaitContainer(ctx context.Context, name string) (int, error) {
	if deadline, ok := ctx.Deadline(); ok {
		f.mu.Lock()
		f.waitDeadline = deadline
		f.mu.Unlock()
	}
	if err := f.record(ctx, "wait "+name); err != nil {
		return 0, err
	}
	if f.waitForContext {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	return f.exitCode, f.waitErr
}

func (f *fakeRuntime) Logs(ctx context.Context, name string) (container.Logs, error) {
	if err := f.record(ctx, "logs "+name); err != nil {
		return container.Logs{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs[name], nil
}

func (f *fakeRuntime) RemoveContainer(ctx context.Context, name string) error {
	if err := f.record(ctx, "remove "+name); err != nil {
		return err
	}
	return f.markGone(name)
}

// markGone removes an object, or reports why it could not be.
func (f *fakeRuntime) markGone(name string) error {
	if f.removeErr != nil {
		return f.removeErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, name)
	return nil
}

// alive reports whether the named object still exists.
func (f *fakeRuntime) alive(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.live[name]
}

// existing stands in for objects another run already owns.
func (f *fakeRuntime) existing(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, name := range names {
		f.live[name] = true
	}
}

var _ container.Runtime = (*fakeRuntime)(nil)
