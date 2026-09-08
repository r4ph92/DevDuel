package store

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound means the row asked for is not there. Callers compare against
// this rather than against pgx.ErrNoRows, so that no part of the system above
// this package has to know which driver is underneath.
var ErrNotFound = errors.New("store: not found")

// ErrEmailTaken and ErrUsernameTaken mean registration collided with an
// account that already exists.
//
// The store distinguishes them because it has to: an operator reading logs
// needs to know which one it was. The HTTP layer must collapse both into one
// message that says neither, since an error that says "this email is taken"
// turns registration into a way of asking whether somebody has an account.
var (
	ErrEmailTaken    = errors.New("store: email is taken")
	ErrUsernameTaken = errors.New("store: username is taken")
)

// uniqueViolation is the SQLSTATE for a broken unique constraint.
const uniqueViolation = "23505"

// translate turns a driver error into one of this package's, so that a caller
// can react to "already taken" or "not there" without knowing the name of a
// constraint or the identity of the driver. Anything it does not recognise it
// passes through unchanged.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case "users_email_key":
			// Returned bare rather than wrapped: the driver's detail carries
			// the address that collided, and this error is on its way to a
			// log line.
			return ErrEmailTaken
		case "users_username_key":
			return ErrUsernameTaken
		}
	}
	return err
}

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
