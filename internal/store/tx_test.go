package store_test

import (
	"errors"
	"testing"

	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// errClosure is what a closure returns when the point of the test is that it
// failed, rather than how.
var errClosure = errors.New("the closure said no")

func TestInTxCommitsWhatTheClosureDid(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	err := db.InTx(ctx, func(q *store.Queries) error {
		_, err := q.CreateUser(ctx, newUser("committed"))
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	if _, err := db.UserByEmail(ctx, "committed@example.test"); err != nil {
		t.Errorf("the committed user is not there: %v", err)
	}
}

func TestInTxRollsBackWhenTheClosureFails(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	err := db.InTx(ctx, func(q *store.Queries) error {
		if _, err := q.CreateUser(ctx, newUser("discarded")); err != nil {
			return err
		}
		return errClosure
	})
	if !errors.Is(err, errClosure) {
		t.Fatalf("InTx = %v, want the closure's own error", err)
	}

	// The closure's error is the caller's to handle, so it must not be
	// dressed up on the way out, and its work must be gone.
	if _, err := db.UserByEmail(ctx, "discarded@example.test"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UserByEmail = %v, want ErrNotFound", err)
	}
}

// A panic through a transaction has to roll back too, or a handler that
// recovers leaves half a change committed.
func TestInTxRollsBackAPanicAndKeepsPanicking(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	defer func() {
		if recover() == nil {
			t.Fatal("the panic did not carry on past InTx")
		}
		if _, err := db.UserByEmail(ctx, "panicked@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("UserByEmail = %v, want ErrNotFound", err)
		}
	}()

	_ = db.InTx(ctx, func(q *store.Queries) error {
		if _, err := q.CreateUser(ctx, newUser("panicked")); err != nil {
			return err
		}
		panic("something went wrong halfway through")
	})
}

// An operation that manages its own atomicity has to be usable as one step of
// a larger one. Inside a transaction the inner call is a savepoint, so its
// failure undoes its own work and nothing else.
func TestInTxNestsAsASavepoint(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	err := db.InTx(ctx, func(q *store.Queries) error {
		if _, err := q.CreateUser(ctx, newUser("outer")); err != nil {
			return err
		}

		inner := q.InTx(ctx, func(q *store.Queries) error {
			if _, err := q.CreateUser(ctx, newUser("inner")); err != nil {
				return err
			}
			return errClosure
		})
		if !errors.Is(inner, errClosure) {
			t.Errorf("inner InTx = %v, want the closure's own error", inner)
		}

		// The outer transaction is still usable, which is the part a failed
		// transaction rather than a savepoint would have broken.
		_, err := q.CreateUser(ctx, newUser("after"))
		return err
	})
	if err != nil {
		t.Fatalf("outer InTx: %v", err)
	}

	for name, want := range map[string]bool{"outer": true, "after": true, "inner": false} {
		_, err := db.UserByEmail(ctx, name+"@example.test")
		if got := err == nil; got != want {
			t.Errorf("%s exists = %t, want %t (err %v)", name, got, want, err)
		}
	}
}
