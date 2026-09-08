package store_test

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// digest stands in for whatever the auth package hashes a real token with.
func digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func TestASessionFindsItsUser(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	user, err := db.CreateUser(ctx, newUser("signedin"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token := digest("a token")
	if err := db.CreateSession(ctx, token, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := db.SessionUser(ctx, token)
	if err != nil {
		t.Fatalf("SessionUser: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("SessionUser returned %s, want %s", got.ID, user.ID)
	}
}

// An expired session has to read as no session at all, without the caller
// having to remember to compare the time.
func TestAnExpiredSessionIsNotFound(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	user, err := db.CreateUser(ctx, newUser("expired"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token := digest("a token that has run out")
	if err := db.CreateSession(ctx, token, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Aged rather than inserted expired, because the schema refuses a session
	// that was over before it began. This is a login from two hours ago that
	// ran out an hour ago.
	const age = `update sessions
		set created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
		where token_hash = $1`
	if _, err := db.Pool().Exec(ctx, age, token); err != nil {
		t.Fatalf("age the session: %v", err)
	}

	if _, err := db.SessionUser(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionUser = %v, want ErrNotFound", err)
	}
}

func TestAnUnknownSessionIsNotFound(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)

	if _, err := db.SessionUser(t.Context(), digest("never issued")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionUser = %v, want ErrNotFound", err)
	}
}

func TestDeletingASessionSignsItOut(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	user, err := db.CreateUser(ctx, newUser("signingout"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token := digest("a token to throw away")
	if err := db.CreateSession(ctx, token, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := db.DeleteSession(ctx, token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	if _, err := db.SessionUser(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionUser = %v, want ErrNotFound", err)
	}

	// Signing out twice is what a client with a stale tab does.
	if err := db.DeleteSession(ctx, token); err != nil {
		t.Errorf("DeleteSession on a session that is gone: %v", err)
	}
}

// Deleting the account takes its logins with it. A session that outlived its
// user would authenticate a request as somebody who no longer exists.
func TestSessionsGoWithTheirUser(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	user, err := db.CreateUser(ctx, newUser("deleted"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token := digest("a token about to be orphaned")
	if err := db.CreateSession(ctx, token, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := db.Pool().Exec(ctx, `delete from users where id = $1`, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if _, err := db.SessionUser(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionUser = %v, want ErrNotFound", err)
	}
}

func TestASessionTokenMustBeADigest(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	user, err := db.CreateUser(ctx, newUser("shorttoken"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// The column is sized for SHA-256, so storing a raw token by mistake is
	// refused rather than accepted and never noticed.
	err = db.CreateSession(ctx, []byte("not a digest"), user.ID, time.Now().Add(time.Hour))
	if err == nil {
		t.Error("CreateSession accepted something that is not a digest, want a refusal")
	}
}

func TestASessionMustOutliveItsCreation(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)

	err := db.CreateSession(t.Context(), digest("stillborn"), id.New(), time.Now().Add(-time.Hour))
	if err == nil {
		t.Error("CreateSession accepted a session that had already expired, want a refusal")
	}
}
