// Package finalizer ends matches whose time is up.
//
// A match's deadline is a timestamp in the database, not a countdown running
// anywhere, so nothing happens at the moment it passes unless something looks.
// This is the something that looks: it ticks, asks for the matches that are
// due, and moves them on. The transition itself is a compare and swap in the
// store, so a tick that races a player's final submission loses harmlessly.
//
// Being late is survivable and being wrong is not. A tick that fails is
// logged and the next one tries again, because the alternative, a worker that
// exits on a transient database error, stops every future deadline too.
package finalizer

import (
	"context"
	"log/slog"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
)

// Expirer is the single thing this package needs from the store. It is
// declared here rather than imported so that the loop can be tested without a
// database, and so that what the worker is trusted to do stays this narrow.
type Expirer interface {
	// ExpireDue moves up to limit matches past their deadline and returns
	// the ones it moved.
	ExpireDue(ctx context.Context, limit int) ([]id.ID, error)
}

// Defaults for a worker that was not told otherwise. Five seconds is the
// precision of the deadline, which is invisible next to a match measured in
// tens of minutes, and a hundred matches is a batch that no single statement
// holds locks for long.
const (
	DefaultInterval = 5 * time.Second
	DefaultBatch    = 100
)

// Config is what a Finalizer needs.
type Config struct {
	// Matches is the store the deadlines live in. Required.
	Matches Expirer
	// Logger receives one line per match that ran out of time. Required.
	Logger *slog.Logger
	// Interval is how often to look. Zero means [DefaultInterval].
	Interval time.Duration
	// Batch is how many matches one pass may expire. Zero means
	// [DefaultBatch].
	Batch int
}

// Finalizer ends matches whose deadline has passed.
type Finalizer struct {
	matches  Expirer
	log      *slog.Logger
	interval time.Duration
	batch    int
}

// New returns a Finalizer, filling in the defaults for anything left zero.
func New(cfg Config) *Finalizer {
	f := &Finalizer{
		matches:  cfg.Matches,
		log:      cfg.Logger,
		interval: cfg.Interval,
		batch:    cfg.Batch,
	}
	if f.interval <= 0 {
		f.interval = DefaultInterval
	}
	if f.batch <= 0 {
		f.batch = DefaultBatch
	}
	return f
}

// Run ticks until ctx is cancelled, and returns nil when it is: being asked
// to stop is not a failure.
//
// It looks once before waiting, so a worker that has just started does not
// leave an already expired match sitting for a full interval.
func (f *Finalizer) Run(ctx context.Context) error {
	f.log.Info("finalizer started", "interval", f.interval, "batch", f.batch)

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		f.tick(ctx)

		select {
		case <-ctx.Done():
			f.log.Info("finalizer stopped")
			return nil
		case <-ticker.C:
		}
	}
}

// tick expires everything that is due, and keeps going while it is filling
// its batch.
//
// The catch-up matters after the worker has been down: with a single pass per
// interval, a backlog of a thousand matches would take a thousand intervals
// to clear, and every one of those matches is a player waiting.
func (f *Finalizer) tick(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		expired, err := f.matches.ExpireDue(ctx, f.batch)
		if err != nil {
			// Cancellation arrives here as an error too, and a worker that is
			// shutting down has nothing to complain about.
			if ctx.Err() == nil {
				f.log.Error("cannot expire matches", "error", err)
			}
			return
		}

		for _, match := range expired {
			f.log.Info("match ran out of time", "match", match)
		}

		// A short pass means the queue is drained. A full one means there may
		// be more waiting, and waiting is the thing to avoid.
		if len(expired) < f.batch {
			return
		}
	}
}
