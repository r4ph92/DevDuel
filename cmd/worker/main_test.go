package main

import (
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/finalizer"
)

const someURL = "postgres://devduel:devduel@localhost:5432/devduel?sslmode=disable"

func TestConfigDefaultsToSomethingRunnable(t *testing.T) {
	t.Setenv("DATABASE_URL", someURL)
	t.Setenv("FINALIZER_INTERVAL", "")
	t.Setenv("FINALIZER_BATCH", "")

	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if cfg.interval != finalizer.DefaultInterval {
		t.Errorf("interval = %v, want the default %v", cfg.interval, finalizer.DefaultInterval)
	}
	if cfg.batch != finalizer.DefaultBatch {
		t.Errorf("batch = %d, want the default %d", cfg.batch, finalizer.DefaultBatch)
	}
}

func TestConfigNeedsADatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	_, err := configFromEnv()
	if err == nil {
		t.Fatal("configFromEnv succeeded with no database, want an error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error = %q, want it to name DATABASE_URL", err)
	}
}

func TestConfigReadsTheEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", someURL)
	t.Setenv("FINALIZER_INTERVAL", "250ms")
	t.Setenv("FINALIZER_BATCH", "7")

	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if cfg.interval != 250*time.Millisecond {
		t.Errorf("interval = %v, want 250ms", cfg.interval)
	}
	if cfg.batch != 7 {
		t.Errorf("batch = %d, want 7", cfg.batch)
	}
}

// How often deadlines are noticed, and how much one statement locks, are not
// settings to guess at when the value is nonsense.
func TestConfigRefusesNonsenseSettings(t *testing.T) {
	for name, env := range map[string]struct{ key, value string }{
		"interval that is not a duration": {"FINALIZER_INTERVAL", "soon"},
		"interval of zero":                {"FINALIZER_INTERVAL", "0s"},
		"negative interval":               {"FINALIZER_INTERVAL", "-5s"},
		"batch that is not a number":      {"FINALIZER_BATCH", "lots"},
		"batch of zero":                   {"FINALIZER_BATCH", "0"},
		"negative batch":                  {"FINALIZER_BATCH", "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", someURL)
			t.Setenv(env.key, env.value)

			if _, err := configFromEnv(); err == nil {
				t.Errorf("configFromEnv accepted %s=%q, want an error", env.key, env.value)
			}
		})
	}
}
