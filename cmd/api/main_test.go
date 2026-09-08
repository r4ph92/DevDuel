package main

import (
	"strings"
	"testing"
)

const someURL = "postgres://devduel:devduel@localhost:5432/devduel?sslmode=disable"

func TestConfigDefaultsToSomethingRunnable(t *testing.T) {
	t.Setenv("DATABASE_URL", someURL)
	t.Setenv("API_ADDR", "")
	t.Setenv("COOKIE_SECURE", "")

	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if cfg.addr != ":8080" {
		t.Errorf("addr = %q, want :8080", cfg.addr)
	}
	// Secure by default, so that forgetting to configure it fails on a
	// laptop rather than in front of the internet.
	if !cfg.secureCookies {
		t.Error("secure cookies are off by default, want on")
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
	t.Setenv("API_ADDR", "127.0.0.1:9999")
	t.Setenv("COOKIE_SECURE", "false")

	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if cfg.addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q, want 127.0.0.1:9999", cfg.addr)
	}
	if cfg.secureCookies {
		t.Error("secure cookies are on, want them off")
	}
}

// A typo in this setting decides whether sessions travel over plaintext, so
// it fails loudly rather than falling back to a default.
func TestConfigRefusesANonBooleanCookieSetting(t *testing.T) {
	t.Setenv("DATABASE_URL", someURL)
	t.Setenv("COOKIE_SECURE", "yes please")

	if _, err := configFromEnv(); err == nil {
		t.Fatal("configFromEnv accepted a non-boolean, want an error")
	}
}
