// Command api serves DevDuel's HTTP API.
//
// It does not migrate on boot. Several instances starting at once would all
// try, and a schema change is a thing somebody should be watching, so that is
// `devduelctl db migrate`.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/r4ph92/DevDuel/internal/api"
	"github.com/r4ph92/DevDuel/internal/auth"
	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/match"
	"github.com/r4ph92/DevDuel/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// SIGTERM as well as interrupt: that is what a container runtime sends,
	// and a match in progress is worth the few seconds of draining.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

// shutdownGrace is how long an in-flight request has to finish once the
// process has been asked to stop.
const shutdownGrace = 20 * time.Second

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

	// Registering the catalog is idempotent, unlike migrating, so every
	// instance does it as it starts rather than waiting for an operator. A
	// challenge version that has changed without its version being bumped
	// stops the process here, before anything can play it.
	catalog, err := challenge.LoadAll(cfg.challengeDir)
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return fmt.Errorf("no challenges in %s: there would be nothing to play", cfg.challengeDir)
	}
	for _, spec := range catalog {
		// The starting workspace is registered with the challenge, so that
		// starting a match later is a database operation and no instance
		// needs this directory on disk to hand a player their files.
		files, err := spec.Workspace()
		if err != nil {
			return fmt.Errorf("read the starting workspace for %s: %w", spec.Key(), err)
		}
		if err := db.RegisterChallenge(ctx, spec, files); err != nil {
			return fmt.Errorf("register %s: %w", spec.Key(), err)
		}
	}
	log.Info("challenge catalog registered", "challenges", len(catalog), "dir", cfg.challengeDir)

	srv := &http.Server{
		Addr: cfg.addr,
		Handler: api.New(api.Config{
			Auth:          auth.NewService(db),
			Match:         match.NewService(db, catalog),
			Logger:        log,
			SecureCookies: cfg.secureCookies,
		}),
		// A header that never finishes arriving would otherwise hold a
		// connection open for as long as the client likes.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Comfortably longer than a login, which spends around a tenth of a
		// second hashing on purpose.
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Listening is reported before the blocking call, because "started" in a
	// log is otherwise a claim nobody checked.
	log.Info("api listening", "addr", cfg.addr, "secure_cookies", cfg.secureCookies)

	errs := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	log.Info("api stopping", "grace", shutdownGrace)

	// A fresh context: the one that was cancelled is what asked for this.
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-errs
}

// config is what the process was told to be.
type config struct {
	addr          string
	databaseURL   string
	challengeDir  string
	secureCookies bool
}

// Environment the api reads.
const (
	envAddr          = "API_ADDR"
	envDatabaseURL   = "DATABASE_URL"
	envChallengeDir  = "CHALLENGES_DIR"
	envSecureCookies = "COOKIE_SECURE"
)

func configFromEnv() (config, error) {
	cfg := config{
		addr:        orElse(os.Getenv(envAddr), ":8080"),
		databaseURL: os.Getenv(envDatabaseURL),
		// The repository layout, which is what a developer running this from
		// a checkout has. An image sets it to wherever it copied them.
		challengeDir: orElse(os.Getenv(envChallengeDir), "challenges"),
		// Secure by default, so that forgetting to configure it fails on a
		// developer's laptop rather than in front of the internet.
		secureCookies: true,
	}

	if cfg.databaseURL == "" {
		return config{}, fmt.Errorf("$%s is not set", envDatabaseURL)
	}

	if raw := os.Getenv(envSecureCookies); raw != "" {
		secure, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("$%s is %q, want a boolean", envSecureCookies, raw)
		}
		cfg.secureCookies = secure
	}
	return cfg, nil
}

// orElse returns value, or fallback when value is empty.
func orElse(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
