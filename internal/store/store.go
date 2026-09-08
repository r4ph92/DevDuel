// Package store owns DevDuel's Postgres schema, its migrations and the typed
// queries everything else reaches the database through.
//
// The schema is the durable half of the system. A workspace is a table here
// rather than a container, a match's clock is two timestamps here rather than
// a countdown in a client, and a match's history is an append only log here
// rather than whatever the WebSocket happened to deliver. Three invariants
// are enforced by the schema itself, in 0001_init.sql, because discovering
// them broken later is expensive: challenges are immutable, match_events is
// append only with a gapless per match sequence, and a player has at most one
// judge job in flight.
//
// Queries carries the typed methods and is bound to either the pool or an
// open transaction, so there is one method per operation rather than one for
// each. [Queries.InTx] is how a change that touches more than one row is
// made: it hands the closure a Queries bound to the transaction, commits when
// the closure returns nil and rolls back on anything else, including a panic.
// Because a Queries that is already inside a transaction begins a savepoint
// instead of a transaction, an operation written that way composes into a
// larger one without either side knowing.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// conn is everything a query needs, and is satisfied by both a pool and an
// open transaction. Begin is part of it because that is what lets the same
// method run standalone or inside a caller's transaction.
type conn interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Queries is the typed query surface, bound to a pool or a transaction.
type Queries struct {
	db conn
}

// Store is a database handle: a pool, the queries that run against it, and
// the migrations that built it.
type Store struct {
	pool *pgxpool.Pool
	*Queries
}

// Open returns a Store for url, having proved that it can reach the database.
//
// Pool sizing is left to the connection string, which pgxpool already reads:
// pool_max_conns, pool_min_conns, pool_max_conn_lifetime,
// pool_max_conn_idle_time and pool_health_check_period. Keeping those in the
// URL means the API and the judge can be tuned separately without a code
// change, since they run as different processes against the same database.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}

	// The pool connects lazily, so without this a wrong host or a wrong
	// password would first surface inside whatever query happened to run
	// first, long after the process reported itself started.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: connect: %w", err)
	}

	return &Store{pool: pool, Queries: &Queries{db: pool}}, nil
}

// Close returns every connection. A Store is not usable afterwards.
func (s *Store) Close() { s.pool.Close() }

// Pool is the underlying pool, for the two jobs the typed queries cannot do:
// migrating, and the schema's own tests, which have to reach constraints and
// triggers that no typed method is ever going to call. Application code
// should not need it.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// InTx runs fn inside a transaction, committing when fn returns nil and
// rolling back otherwise. The Queries handed to fn is bound to that
// transaction, so everything done through it either lands together or not at
// all. A panic rolls back too, and then carries on panicking.
//
// Calling InTx on a Queries that is already in a transaction opens a
// savepoint rather than a second transaction, so an operation that manages
// its own atomicity can still be one step of a larger one. The inner call
// undoes only its own work when it fails.
func (q *Queries) InTx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := q.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	// Rollback after a commit reports that the transaction is already closed,
	// which is why this can be unconditional: it covers the error paths and
	// the panic without the happy path having to say anything. The context is
	// detached because a cancelled request still has to release its rows.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(&Queries{db: tx}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
