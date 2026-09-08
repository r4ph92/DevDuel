package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
)

// Event is one entry in a match's log. The log is append only and ordered by
// Seq, which starts at 1 and has no gaps, so a client that reconnects asks
// for everything after the last Seq it saw and knows it missed nothing.
type Event struct {
	Seq       int64
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// AppendEvent adds one event to a match's log and returns the sequence number
// it was given.
//
// The sequence comes from matches.event_seq, bumped in the same transaction
// as the insert. That is what makes it gapless: reading the current maximum
// and adding one hands the same number to two concurrent writers, and a
// sequence with a hole in it leaves a reconnecting client waiting for an
// event nobody ever wrote. Nothing else may allocate a sequence number.
func (q *Queries) AppendEvent(ctx context.Context, match id.ID, kind string, payload json.RawMessage) (int64, error) {
	// The column is not nullable, and a reader should never have to handle
	// two spellings of "no payload".
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	var seq int64
	err := q.InTx(ctx, func(q *Queries) error {
		// The update locks the match row, so a second writer waits here
		// rather than racing to the same number.
		const allocate = `update matches set event_seq = event_seq + 1
			where id = $1 returning event_seq`
		if err := q.db.QueryRow(ctx, allocate, match).Scan(&seq); err != nil {
			return translate(err)
		}

		const insert = `insert into match_events (match_id, seq, type, payload)
			values ($1, $2, $3, $4)`
		if _, err := q.db.Exec(ctx, insert, match, seq, kind, payload); err != nil {
			return translate(err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// EventsAfter returns a match's events in order, starting from the one after
// seq. Passing 0 returns the whole log, which is what a client that has seen
// nothing asks for.
func (q *Queries) EventsAfter(ctx context.Context, match id.ID, seq int64) ([]Event, error) {
	const query = `select seq, type, payload, created_at from match_events
		where match_id = $1 and seq > $2 order by seq`

	rows, err := q.db.Query(ctx, query, match, seq)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: read match event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read match events: %w", err)
	}
	return out, nil
}
