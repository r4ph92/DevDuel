package store

import (
	"context"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
)

// DefaultRating is where a player starts. M6 owns how ratings move; all this
// says is that everybody begins at the same place, and 1200 is the number
// most rating systems use for that.
const DefaultRating = 1200

// User is an account, and deliberately has nowhere to put a password hash.
// A struct that cannot carry the hash cannot leak it into a log line or a
// JSON response by being handed to the wrong writer. Login reads the hash
// through [Queries.CredentialsByEmail] instead, which returns it on its own
// and is the only thing in the system that does.
type User struct {
	ID        id.ID
	Email     string
	Username  string
	CreatedAt time.Time
}

// Credentials is what a login attempt is checked against.
type Credentials struct {
	UserID id.ID
	// Hash is whatever the hashing scheme encodes, in its own self
	// describing format. Nothing may log it or return it over HTTP.
	Hash string
}

// NewUser is a registration.
type NewUser struct {
	Email    string
	Username string
	// PasswordHash is already hashed. This package never sees a password.
	PasswordHash string
}

// Rating is a player's standing.
type Rating struct {
	UserID    id.ID
	Value     int
	Games     int
	UpdatedAt time.Time
}

// CreateUser registers an account and opens its rating in one transaction.
//
// The two rows go together because a user without a rating is a user the
// matchmaker cannot rank, and a rating with no user is a row nothing will
// ever read. Doing it here rather than expecting two calls in the right order
// is what the transaction helper is for.
func (q *Queries) CreateUser(ctx context.Context, in NewUser) (User, error) {
	var out User
	err := q.InTx(ctx, func(q *Queries) error {
		// Stored as it was typed, since a person's capitalisation of their
		// own address is theirs. Uniqueness and lookup are case insensitive,
		// which is the index on lower(email) in 0001_init.sql.
		const insertUser = `insert into users (id, email, username, password_hash)
			values ($1, $2, $3, $4)
			returning id, email, username, created_at`

		user := id.New()
		row := q.db.QueryRow(ctx, insertUser, user, in.Email, in.Username, in.PasswordHash)
		if err := row.Scan(&out.ID, &out.Email, &out.Username, &out.CreatedAt); err != nil {
			return translate(err)
		}

		const insertRating = `insert into ratings (user_id, rating) values ($1, $2)`
		if _, err := q.db.Exec(ctx, insertRating, out.ID, DefaultRating); err != nil {
			return translate(err)
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return out, nil
}

const selectUser = `select id, email, username, created_at from users`

// UserByID returns the account, or [ErrNotFound].
func (q *Queries) UserByID(ctx context.Context, user id.ID) (User, error) {
	var out User
	row := q.db.QueryRow(ctx, selectUser+` where id = $1`, user)
	if err := row.Scan(&out.ID, &out.Email, &out.Username, &out.CreatedAt); err != nil {
		return User{}, translate(err)
	}
	return out, nil
}

// UserByEmail returns the account, matching the address case insensitively so
// that a login cannot fail over capitalisation that registration allowed.
func (q *Queries) UserByEmail(ctx context.Context, email string) (User, error) {
	var out User
	row := q.db.QueryRow(ctx, selectUser+` where lower(email) = lower($1)`, email)
	if err := row.Scan(&out.ID, &out.Email, &out.Username, &out.CreatedAt); err != nil {
		return User{}, translate(err)
	}
	return out, nil
}

// CredentialsByEmail returns the stored hash for a login attempt, or
// [ErrNotFound]. It is the only way to read a password hash, so a search for
// its callers is a complete audit of where hashes go.
func (q *Queries) CredentialsByEmail(ctx context.Context, email string) (Credentials, error) {
	const query = `select id, password_hash from users where lower(email) = lower($1)`

	var out Credentials
	if err := q.db.QueryRow(ctx, query, email).Scan(&out.UserID, &out.Hash); err != nil {
		return Credentials{}, translate(err)
	}
	return out, nil
}

// Rating returns a player's standing, or [ErrNotFound].
func (q *Queries) Rating(ctx context.Context, user id.ID) (Rating, error) {
	const query = `select user_id, rating, games, updated_at from ratings where user_id = $1`

	var out Rating
	err := q.db.QueryRow(ctx, query, user).Scan(&out.UserID, &out.Value, &out.Games, &out.UpdatedAt)
	if err != nil {
		return Rating{}, translate(err)
	}
	return out, nil
}
