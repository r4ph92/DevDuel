package store

import "errors"

// ErrChecksumMismatch means a migration that the database has already applied
// no longer matches the file of the same version. Editing a committed
// migration leaves every database that ran the old text disagreeing with
// every database that runs the new one, so this is a hard stop rather than a
// warning: the fix is a new migration.
var ErrChecksumMismatch = errors.New("store: applied migration has changed")

// ErrUnknownMigration means the database has applied a migration this binary
// does not carry, which is what a rollback to an older build looks like.
// Migrations only go forward, so the old binary must not run against it.
var ErrUnknownMigration = errors.New("store: database is newer than this binary")

// ErrBadMigration means the embedded migrations are not a usable set: a name
// that does not carry a version, a duplicate version, or a gap where a file
// was never committed.
var ErrBadMigration = errors.New("store: malformed migration set")
