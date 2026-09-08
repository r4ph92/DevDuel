// Package storetest hands a test its own Postgres database.
//
// Schema tests are worth having only if they run against a real Postgres:
// every invariant in 0001_init.sql is a constraint, a partial index or a
// trigger, and none of those exist in a fake. So each test gets a database of
// its own, cloned from a template that was migrated once.
//
// Cloning is what makes that affordable. CREATE DATABASE ... TEMPLATE copies
// an already migrated database file by file, which costs tens of
// milliseconds, where running every migration per test would cost the whole
// suite. Real databases rather than shared transactions also mean a test can
// exercise the transactions of the code under test, and can run in parallel
// with every other test.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r4ph92/DevDuel/internal/store"
)

// EnvURL names the database the test databases are created next to. CI sets
// it to a service container; locally it comes from .env and docker compose.
const EnvURL = "DATABASE_URL"

// templateDB is migrated once and cloned per test.
const templateDB = "devduel_test_template"

// templateLock serialises template creation across test binaries. Each Go
// package is its own process, so a mutex is not enough: `go test ./...`
// runs several of them at once against the same server.
const templateLock int64 = 0x64657664_74706c74 // "devdtplt"

// setupTimeout bounds creating and dropping databases, which is fast unless
// the server is not there at all.
const setupTimeout = 30 * time.Second

// New returns a migrated database of this test's own, dropped when the test
// ends.
func New(t *testing.T) *store.Store {
	t.Helper()
	return database(t, templateDB)
}

// Empty returns a database with no schema at all, for tests of migration
// itself.
func Empty(t *testing.T) *store.Store {
	t.Helper()
	return database(t, "")
}

func database(t *testing.T, template string) *store.Store {
	t.Helper()

	url := requireURL(t)
	if template != "" {
		ensureTemplate(t, url)
	}

	ctx, cancel := context.WithTimeout(t.Context(), setupTimeout)
	defer cancel()

	name := databaseName(t)
	target, err := replaceDatabase(url, name)
	if err != nil {
		t.Fatal(err)
	}
	admin := connect(ctx, t, url)

	// The clone has to be serialised against template creation: Postgres
	// refuses to copy a database that anything else is connected to.
	unlock := lock(ctx, t, admin, templateLock)
	create := fmt.Sprintf("create database %s", quote(name))
	if template != "" {
		create += fmt.Sprintf(" template %s", quote(template))
	}
	if _, err := admin.Exec(ctx, create); err != nil {
		unlock()
		t.Fatalf("create database %s: %v", name, err)
	}
	unlock()

	db, err := store.Open(t.Context(), target)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}

	t.Cleanup(func() {
		// Close first: Postgres will not drop a database with a live
		// connection, and force would only paper over a leaked pool.
		db.Close()

		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), setupTimeout)
		defer cancel()

		if _, err := admin.Exec(ctx, fmt.Sprintf("drop database if exists %s (force)", quote(name))); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})

	return db
}

// templateOnce keeps one test binary from building the template repeatedly,
// and templateErr carries the outcome to the callers that did not build it.
// Without the second variable every test after the first would read a nil
// error and go on to fail on a template that was never created.
var (
	templateOnce sync.Once
	templateErr  error
)

func ensureTemplate(t *testing.T, url string) {
	t.Helper()

	templateOnce.Do(func() { templateErr = buildTemplate(t, url) })
	if templateErr != nil {
		t.Fatalf("build template database: %v", templateErr)
	}
}

// buildTemplate brings the template database up to the current migrations,
// creating it if it is missing. Both halves are idempotent, which is what
// lets a second test binary find the template already there and simply bring
// it up to date.
func buildTemplate(t *testing.T, url string) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), setupTimeout)
	defer cancel()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()

	if _, err := admin.Exec(ctx, "select pg_advisory_lock($1)", templateLock); err != nil {
		return fmt.Errorf("take template lock: %w", err)
	}
	defer func() {
		_, _ = admin.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", templateLock)
	}()

	err = migrateTemplate(ctx, admin, url)
	if !errors.Is(err, store.ErrChecksumMismatch) && !errors.Is(err, store.ErrUnknownMigration) {
		return err
	}

	// The template is a cache of the migrations, and the migrations have
	// moved underneath it: either one was edited while it was still being
	// written, or a branch with fewer of them is now checked out. Both are
	// normal while developing, and neither is worth making somebody drop a
	// database by hand, so the cache is thrown away and rebuilt. Nothing
	// outside these tests is allowed to react to that error this way.
	if _, derr := admin.Exec(ctx, "drop database if exists "+quote(templateDB)+" (force)"); derr != nil {
		return fmt.Errorf("rebuild stale template: %w", derr)
	}
	return migrateTemplate(ctx, admin, url)
}

// migrateTemplate creates the template database if it does not exist and
// applies every migration to it. The connection it opens is closed before it
// returns, because Postgres will not clone a database that anything is
// connected to.
func migrateTemplate(ctx context.Context, admin *pgx.Conn, url string) error {
	var exists bool
	const query = "select exists (select 1 from pg_database where datname = $1)"
	if err := admin.QueryRow(ctx, query, templateDB).Scan(&exists); err != nil {
		return fmt.Errorf("look for template: %w", err)
	}
	if !exists {
		if _, err := admin.Exec(ctx, "create database "+quote(templateDB)); err != nil {
			return fmt.Errorf("create template: %w", err)
		}
	}

	target, err := replaceDatabase(url, templateDB)
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, target)
	if err != nil {
		return fmt.Errorf("open template: %w", err)
	}
	defer db.Close()

	if _, err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	return nil
}

// requireURL skips rather than fails when there is no database, the same way
// the judge's tests skip when there is no Docker daemon. CI sets DATABASE_URL
// so that this never silently skips there.
func requireURL(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("database test skipped in -short mode")
	}
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s is not set: start the compose services and export it", EnvURL)
	}
	return url
}

func connect(ctx context.Context, t *testing.T, url string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvURL, err)
	}
	return conn
}

// lock takes an advisory lock and returns the release, which is idempotent so
// that a caller can release early on an error path and still defer it.
func lock(ctx context.Context, t *testing.T, conn *pgx.Conn, key int64) func() {
	t.Helper()

	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", key); err != nil {
		t.Fatalf("take advisory lock: %v", err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			_, err := conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", key)
			if err != nil {
				t.Errorf("release advisory lock: %v", err)
			}
		})
	}
}

// unsafeName is everything a database name may not contain here. Names are
// built from test names, and a test name can hold anything at all.
var unsafeName = regexp.MustCompile(`[^a-z0-9_]+`)

// databaseName is short, unique, and says which test owns it, so that a
// leaked database can be traced back to the test that leaked it.
func databaseName(t *testing.T) string {
	t.Helper()

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}

	base := unsafeName.ReplaceAllString(strings.ToLower(t.Name()), "_")
	// Postgres truncates identifiers at 63 bytes, which would collapse two
	// long test names onto one database.
	if len(base) > 40 {
		base = base[:40]
	}
	return "devduel_test_" + base + "_" + hex.EncodeToString(suffix[:])
}

// quote makes an identifier safe to interpolate. Database names cannot be
// parameters, so they are quoted instead.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// replaceDatabase points a connection string at another database on the same
// server, keeping the credentials and every option it already carries. Only
// the URL form is handled, because that is what .env and CI both use.
func replaceDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", EnvURL, err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("%s must be a postgres:// url, not %q", EnvURL, u.Scheme)
	}

	u.Path = "/" + name
	return u.String(), nil
}
