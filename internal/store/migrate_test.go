package store_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

func TestMigrateAppliesTheSchemaAndStopsThere(t *testing.T) {
	t.Parallel()
	db := storetest.Empty(t)
	ctx := t.Context()

	applied, err := db.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("Migrate applied nothing to an empty database")
	}
	if applied[0].Version != 1 || applied[0].Name != "init" {
		t.Errorf("first applied = %d_%s, want 1_init", applied[0].Version, applied[0].Name)
	}

	// The schema is really there, not just recorded.
	var users int
	if err := db.Pool().QueryRow(ctx, "select count(*) from users").Scan(&users); err != nil {
		t.Fatalf("select from users: %v", err)
	}

	// Every process calls Migrate at startup, so a second run has to be a
	// no-op rather than an error.
	again, err := db.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second Migrate applied %d migrations, want 0", len(again))
	}
}

// A deploy and a CI job can migrate the same database at the same moment.
// The advisory lock is what stops the second one from applying the same file
// again on top of the first.
func TestMigrateIsSafeToRunConcurrently(t *testing.T) {
	t.Parallel()
	db := storetest.Empty(t)
	ctx := t.Context()

	const callers = 4
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		total   int
		failure error
	)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied, err := db.Migrate(ctx)

			mu.Lock()
			defer mu.Unlock()
			total += len(applied)
			if err != nil && failure == nil {
				failure = err
			}
		}()
	}
	wg.Wait()

	if failure != nil {
		t.Fatalf("concurrent Migrate: %v", failure)
	}

	var recorded int
	if err := db.Pool().QueryRow(ctx, "select count(*) from schema_migrations").Scan(&recorded); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if recorded == 0 {
		t.Fatal("no migrations were recorded")
	}
	// Each migration was applied by exactly one of the callers.
	if total != recorded {
		t.Errorf("callers applied %d migrations between them, want %d", total, recorded)
	}
}

func TestMigrateRefusesAnAppliedMigrationThatChanged(t *testing.T) {
	t.Parallel()
	db := storetest.Empty(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// What editing a committed migration looks like from the database's side.
	const tamper = "update schema_migrations set checksum = 'not-what-ran' where version = 1"
	if _, err := db.Pool().Exec(ctx, tamper); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, err := db.Migrate(ctx)
	if !errors.Is(err, store.ErrChecksumMismatch) {
		t.Fatalf("Migrate = %v, want ErrChecksumMismatch", err)
	}
}

func TestMigrateRefusesADatabaseFromTheFuture(t *testing.T) {
	t.Parallel()
	db := storetest.Empty(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// A newer build migrated this database, and then was rolled back to this
	// one. Migrations only go forward, so this binary must not touch it.
	const ahead = `insert into schema_migrations (version, name, checksum)
	                values (9999, 'from_the_future', 'unknown')`
	if _, err := db.Pool().Exec(ctx, ahead); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}

	_, err := db.Migrate(ctx)
	if !errors.Is(err, store.ErrUnknownMigration) {
		t.Fatalf("Migrate = %v, want ErrUnknownMigration", err)
	}
}
