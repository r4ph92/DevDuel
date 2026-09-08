// Package store owns DevDuel's Postgres schema, its migrations and the
// connection pool everything else borrows from.
//
// The schema is the durable half of the system. A workspace is a table here
// rather than a container, a match's clock is two timestamps here rather than
// a countdown in a client, and a match's history is an append only log here
// rather than whatever the WebSocket happened to deliver. Three invariants
// are enforced by the schema itself, in 0001_init.sql, because discovering
// them broken later is expensive: challenges are immutable, match_events is
// append only with a gapless per match sequence, and a player has at most one
// judge job in flight.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open returns a pool for url, having proved that it can reach the database.
//
// Pool sizing is left to the connection string, which pgxpool already reads:
// pool_max_conns, pool_min_conns, pool_max_conn_lifetime,
// pool_max_conn_idle_time and pool_health_check_period. Keeping those in the
// URL means the API and the judge can be tuned separately without a code
// change, since they run as different processes against the same database.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
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
	return pool, nil
}
