// Command worker runs DevDuel's background work.
//
// Today that is one job: ending matches whose deadline has passed. It lives
// outside the API because a deadline should fire once whether there are three
// API instances or none, and because the judge queue will want the same home.
//
// Like the API, it does not migrate on boot. That is `devduelctl db migrate`.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/r4ph92/DevDuel/internal/finalizer"
	"github.com/r4ph92/DevDuel/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// SIGTERM as well as interrupt: that is what a container runtime sends,
	// and a tick in flight is worth the moment it takes to finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := configFromEnv()
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	return finalizer.New(finalizer.Config{
		Matches:  db,
		Logger:   log,
		Interval: cfg.interval,
		Batch:    cfg.batch,
	}).Run(ctx)
}

// config is what the process was told to be.
type config struct {
	databaseURL string
	interval    time.Duration
	batch       int
}

// Environment the worker reads.
const (
	envDatabaseURL = "DATABASE_URL"
	envInterval    = "FINALIZER_INTERVAL"
	envBatch       = "FINALIZER_BATCH"
)

func configFromEnv() (config, error) {
	cfg := config{
		databaseURL: os.Getenv(envDatabaseURL),
		interval:    finalizer.DefaultInterval,
		batch:       finalizer.DefaultBatch,
	}

	if cfg.databaseURL == "" {
		return config{}, fmt.Errorf("$%s is not set", envDatabaseURL)
	}

	// Both settings decide how quickly a deadline is noticed and how much one
	// statement locks, so a typo in either fails here rather than quietly
	// running at a value nobody chose.
	if raw := os.Getenv(envInterval); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("$%s is %q, want a duration like 5s", envInterval, raw)
		}
		if interval <= 0 {
			return config{}, fmt.Errorf("$%s is %q, want a positive duration", envInterval, raw)
		}
		cfg.interval = interval
	}

	if raw := os.Getenv(envBatch); raw != "" {
		batch, err := strconv.Atoi(raw)
		if err != nil {
			return config{}, fmt.Errorf("$%s is %q, want a whole number", envBatch, raw)
		}
		if batch <= 0 {
			return config{}, fmt.Errorf("$%s is %q, want a positive number", envBatch, raw)
		}
		cfg.batch = batch
	}

	return cfg, nil
}
