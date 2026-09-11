package finalizer_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/finalizer"
	"github.com/r4ph92/DevDuel/internal/id"
)

// fakeMatches is a scripted store. Each call takes the next answer, and the
// last one repeats, so a test says what the first ticks see without having to
// count how many happen.
type fakeMatches struct {
	mu      sync.Mutex
	answers []answer
	calls   int
	limits  []int
	called  chan struct{}
}

type answer struct {
	expired []id.ID
	err     error
}

func newFake(answers ...answer) *fakeMatches {
	return &fakeMatches{answers: answers, called: make(chan struct{}, 64)}
}

func (f *fakeMatches) ExpireDue(_ context.Context, limit int) ([]id.ID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.limits = append(f.limits, limit)
	a := f.answers[min(f.calls, len(f.answers)-1)]
	f.calls++

	select {
	case f.called <- struct{}{}:
	default:
	}
	return a.expired, a.err
}

func (f *fakeMatches) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeMatches) limitsSeen() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.limits...)
}

// waitForCalls blocks until the fake has been called n times, or fails.
func (f *fakeMatches) waitForCalls(t *testing.T, n int) {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for f.callCount() < n {
		select {
		case <-f.called:
		case <-deadline:
			t.Fatalf("the finalizer made %d calls, want %d", f.callCount(), n)
		}
	}
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// run starts the finalizer and returns a function that stops it and waits.
func run(t *testing.T, f *finalizer.Finalizer) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v, want nil on cancellation", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
	}
}

// A match whose deadline has passed is ended without anybody touching it,
// which is the whole point of the worker.
func TestTheFinalizerExpiresWhatIsDue(t *testing.T) {
	t.Parallel()
	due := []id.ID{id.New(), id.New()}
	fake := newFake(answer{expired: due}, answer{})

	stop := run(t, finalizer.New(finalizer.Config{
		Matches:  fake,
		Logger:   discard(),
		Interval: 5 * time.Millisecond,
		Batch:    10,
	}))
	fake.waitForCalls(t, 2)
	stop()

	for _, limit := range fake.limitsSeen() {
		if limit != 10 {
			t.Errorf("asked for %d matches, want the configured batch of 10", limit)
		}
	}
}

// It looks once on startup rather than waiting out a full interval, so a
// worker that was restarted does not leave an expired match sitting.
func TestTheFinalizerLooksBeforeItWaits(t *testing.T) {
	t.Parallel()
	fake := newFake(answer{})

	stop := run(t, finalizer.New(finalizer.Config{
		Matches: fake,
		Logger:  discard(),
		// Longer than this test will ever wait: the only call it can make is
		// the one before the first tick.
		Interval: time.Hour,
		Batch:    10,
	}))
	fake.waitForCalls(t, 1)
	stop()
}

// A pass that fills its batch means there may be more waiting, and every one
// of those is a player whose match has not ended yet.
func TestAFullBatchKeepsGoingWithinOneTick(t *testing.T) {
	t.Parallel()
	full := []id.ID{id.New(), id.New()}
	fake := newFake(
		answer{expired: full},
		answer{expired: full[:1]},
		answer{},
	)

	stop := run(t, finalizer.New(finalizer.Config{
		Matches: fake,
		Logger:  discard(),
		// Long enough that a second tick cannot explain the extra calls.
		Interval: time.Hour,
		Batch:    2,
	}))
	fake.waitForCalls(t, 2)
	stop()

	if calls := fake.callCount(); calls < 2 {
		t.Errorf("a full batch stopped after %d call, want it to keep draining", calls)
	}
}

// A database that is briefly unreachable must not take the worker with it, or
// one blip stops every future deadline.
func TestATickThatFailsDoesNotStopTheWorker(t *testing.T) {
	t.Parallel()
	fake := newFake(
		answer{err: errors.New("database is having a moment")},
		answer{expired: []id.ID{id.New()}},
		answer{},
	)

	stop := run(t, finalizer.New(finalizer.Config{
		Matches:  fake,
		Logger:   discard(),
		Interval: 5 * time.Millisecond,
		Batch:    10,
	}))
	fake.waitForCalls(t, 3)
	stop()
}

func TestDefaultsFillInWhatWasNotSet(t *testing.T) {
	t.Parallel()
	fake := newFake(answer{})

	stop := run(t, finalizer.New(finalizer.Config{Matches: fake, Logger: discard()}))
	fake.waitForCalls(t, 1)
	stop()

	limits := fake.limitsSeen()
	if len(limits) == 0 || limits[0] != finalizer.DefaultBatch {
		t.Errorf("first pass asked for %v, want the default batch of %d", limits, finalizer.DefaultBatch)
	}
}
