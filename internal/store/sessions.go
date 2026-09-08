package store

import (
	"context"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
)

// Session is a login that has not expired.
type Session struct {
	UserID    id.ID
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSession records a login. The caller passes the digest of the token,
// never the token: this package has no way to reverse one, which is the point
// of storing it that way.
func (q *Queries) CreateSession(ctx context.Context, tokenHash []byte, user id.ID, expires time.Time) error {
	const insert = `insert into sessions (token_hash, user_id, expires_at) values ($1, $2, $3)`

	if _, err := q.db.Exec(ctx, insert, tokenHash, user, expires); err != nil {
		return translate(err)
	}
	return nil
}

// SessionUser returns the account a session belongs to, or [ErrNotFound] when
// the session is unknown or has expired.
//
// Expiry is a condition of the query rather than something the caller checks
// after the fact, so a session that has run out is indistinguishable from one
// that never existed, and no code path can forget to look.
func (q *Queries) SessionUser(ctx context.Context, tokenHash []byte) (User, error) {
	const query = `select u.id, u.email, u.username, u.created_at
		from sessions s join users u on u.id = s.user_id
		where s.token_hash = $1 and s.expires_at > now()`

	var out User
	row := q.db.QueryRow(ctx, query, tokenHash)
	if err := row.Scan(&out.ID, &out.Email, &out.Username, &out.CreatedAt); err != nil {
		return User{}, translate(err)
	}
	return out, nil
}

// DeleteSession signs one login out. Deleting a session that is not there is
// not an error: the caller's intent was that it should be gone, and it is.
func (q *Queries) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := q.db.Exec(ctx, `delete from sessions where token_hash = $1`, tokenHash); err != nil {
		return translate(err)
	}
	return nil
}
