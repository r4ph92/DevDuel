package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Migrations are forward only. There are no down migrations because there is
// no situation in which running one is the right move: undoing a schema
// change on a database that has live rows in it is a new migration, written
// deliberately, not a mechanical reverse of the last one.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

const migrationDir = "migrations"

// migrationLock is the advisory lock every migrating process contends for, so
// that two API instances starting at once apply each migration exactly once.
// The value is arbitrary and only has to be unique within the database.
const migrationLock int64 = 0x64657664_6d696772 // "devdmigr"

// migrationName is <version>_<name>.sql, e.g. 0001_init.sql. The version is
// what orders the set; the name is only there to make the file readable.
var migrationName = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.sql$`)

// Applied is one migration that a call to [Migrate] ran.
type Applied struct {
	Version int
	Name    string
}

// migration is one file in the embedded set.
type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

// Migrate brings the database up to the schema this binary carries, and
// returns the migrations it had to run. Running it against an up to date
// database applies nothing and is not an error, so running it twice, or from
// two places at the same moment, is safe.
func (s *Store) Migrate(ctx context.Context) ([]Applied, error) {
	files, err := loadMigrations(migrationFS)
	if err != nil {
		return nil, err
	}

	// One connection for the whole run: an advisory lock is held by a
	// session, so taking it on a pooled connection and applying migrations on
	// another would leave the work unprotected.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: acquire connection: %w", err)
	}
	defer conn.Release()

	return migrateConn(ctx, conn.Conn(), files)
}

func migrateConn(ctx context.Context, conn *pgx.Conn, files []migration) (applied []Applied, err error) {
	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrationLock); err != nil {
		return nil, fmt.Errorf("store: take migration lock: %w", err)
	}
	defer func() {
		// Not ctx: releasing the lock is exactly the work that still has to
		// happen when the caller has given up, and a pooled connection keeps
		// its session, so an unreleased lock would outlive this process's
		// interest in it.
		_, uerr := conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", migrationLock)
		if uerr != nil {
			err = errors.Join(err, fmt.Errorf("store: release migration lock: %w", uerr))
		}
	}()

	if err := ensureMigrationTable(ctx, conn); err != nil {
		return nil, err
	}

	done, err := appliedMigrations(ctx, conn)
	if err != nil {
		return nil, err
	}
	if err := checkAgainstFiles(files, done); err != nil {
		return nil, err
	}

	for _, m := range files {
		if _, ok := done[m.version]; ok {
			continue
		}
		if err := apply(ctx, conn, m); err != nil {
			return applied, err
		}
		applied = append(applied, Applied{Version: m.version, Name: m.name})
	}
	return applied, nil
}

func ensureMigrationTable(ctx context.Context, conn *pgx.Conn) error {
	const ddl = `create table if not exists schema_migrations (
		version    integer primary key,
		name       text not null,
		checksum   text not null,
		applied_at timestamptz not null default now()
	)`

	if _, err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	return nil
}

// appliedMigrations reads what the database has already run, by version.
func appliedMigrations(ctx context.Context, conn *pgx.Conn) (map[int]string, error) {
	rows, err := conn.Query(ctx, "select version, checksum from schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer rows.Close()

	done := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("store: read schema_migrations: %w", err)
		}
		done[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	return done, nil
}

// checkAgainstFiles refuses to migrate a database that disagrees with the
// files, rather than papering over the disagreement by applying what is left.
func checkAgainstFiles(files []migration, done map[int]string) error {
	byVersion := make(map[int]migration, len(files))
	for _, m := range files {
		byVersion[m.version] = m
	}

	// Sorted, so that a database disagreeing about several migrations always
	// names the same one first.
	for _, version := range slices.Sorted(maps.Keys(done)) {
		checksum := done[version]

		m, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("%w: it has applied migration %d", ErrUnknownMigration, version)
		}
		if m.checksum != checksum {
			return fmt.Errorf("%w: %d_%s.sql was %s when applied and is %s now",
				ErrChecksumMismatch, m.version, m.name, checksum, m.checksum)
		}
	}
	return nil
}

// apply runs one migration and records it, in a single transaction. Postgres
// has transactional DDL, so a migration that fails halfway leaves nothing
// behind, including its schema_migrations row.
func apply(ctx context.Context, conn *pgx.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("store: apply migration %d_%s: %w", m.version, m.name, err)
	}

	const record = `insert into schema_migrations (version, name, checksum) values ($1, $2, $3)`
	if _, err := tx.Exec(ctx, record, m.version, m.name, m.checksum); err != nil {
		return fmt.Errorf("store: record migration %d: %w", m.version, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit migration %d: %w", m.version, err)
	}
	return nil
}

// loadMigrations reads the migration set and checks that it is one: versions
// numbered from 1, no duplicates, no gaps. A gap almost always means a file
// was written and never committed, and applying the rest anyway would produce
// a database no other database matches.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, migrationDir)
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}

	var out []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		match := migrationName.FindStringSubmatch(e.Name())
		if match == nil {
			return nil, fmt.Errorf("%w: %s is not <version>_<name>.sql", ErrBadMigration, e.Name())
		}
		version, err := strconv.Atoi(match[1])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("%w: %s has no usable version", ErrBadMigration, e.Name())
		}

		body, err := fs.ReadFile(fsys, migrationDir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", e.Name(), err)
		}

		sum := sha256.Sum256(body)
		out = append(out, migration{
			version:  version,
			name:     match[2],
			sql:      string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}

	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i, m := range out {
		if want := i + 1; m.version != want {
			return nil, fmt.Errorf("%w: expected migration %d, found %d", ErrBadMigration, want, m.version)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no migrations", ErrBadMigration)
	}
	return out, nil
}
