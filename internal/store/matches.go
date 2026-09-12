package store

import (
	"context"
	"fmt"

	"github.com/r4ph92/DevDuel/internal/id"
)

// The transitions a match makes after its lobby fills.
//
// Every one of them is a compare and swap: the new state is written by an
// update guarded on the old one, and the number of rows it touched says
// whether this caller was the one that moved the match. Both players
// submitting and the deadline firing happen at the same instant by design, so
// the loser of that race has to be a no-op rather than a second transition.
//
// Each transaction takes the match row before it writes a player row. The
// trigger in 0003 updates every seat when a match changes state, so a writer
// that took a seat first and then reached for the match would deadlock
// against one going the other way.

// ReadyUp marks user ready, and starts the match when both players are.
//
// The returned bool is true only for the call that started the match, so the
// caller that has to act on the start (and nobody else) can tell. Readying
// twice is not an error: a client that lost the response asks again.
func (q *Queries) ReadyUp(ctx context.Context, match, user id.ID) (Match, bool, error) {
	var out Match
	var started bool

	err := q.InTx(ctx, func(q *Queries) error {
		current, err := q.lockMatchForPlayer(ctx, match, user)
		if err != nil {
			return err
		}
		if current.State != MatchLobby {
			return ErrMatchStarted
		}
		if len(current.Players) < slots {
			return ErrLobbyIncomplete
		}

		const ready = `update match_players set ready_at = now()
			where match_id = $1 and user_id = $2 and ready_at is null`
		if _, err := q.db.Exec(ctx, ready, match, user); err != nil {
			return translate(err)
		}

		// The deadline is computed here from the challenge's own duration, so
		// that no caller anywhere gets to decide how long a match lasts. The
		// guard on the state is what makes a second caller a no-op.
		const start = `update matches m
			set state = 'active', started_at = now(), deadline_at = now() + c.duration
			from challenges c
			where m.id = $1
			  and c.id = m.challenge_id and c.version = m.challenge_version
			  and m.state = 'lobby'
			  and (select count(*) from match_players p where p.match_id = m.id) = 2
			  and not exists (
				select 1 from match_players p where p.match_id = m.id and p.ready_at is null
			  )`

		tag, err := q.db.Exec(ctx, start, match)
		if err != nil {
			return translate(err)
		}
		started = tag.RowsAffected() == 1

		// Seeded by the call that started the match, in the same transaction,
		// so a match is never briefly active with nothing in it to edit.
		if started {
			if err := q.seedWorkspaces(ctx, match); err != nil {
				return err
			}
		}

		out, err = q.matchByID(ctx, match)
		return err
	})
	if err != nil {
		return Match{}, false, err
	}
	return out, started, nil
}

// Submit records that user is done, and moves the match to judging once both
// players are.
//
// The returned bool is true only for the call that moved it, which is the one
// that will later be responsible for the judging that follows. Submitting
// twice changes nothing, and the second call reports false.
func (q *Queries) Submit(ctx context.Context, match, user id.ID) (Match, bool, error) {
	var out Match
	var judging bool

	err := q.InTx(ctx, func(q *Queries) error {
		current, err := q.lockMatchForPlayer(ctx, match, user)
		if err != nil {
			return err
		}
		if current.State != MatchActive {
			return ErrMatchNotActive
		}

		const submit = `update match_players set submitted_at = now()
			where match_id = $1 and user_id = $2 and submitted_at is null`
		if _, err := q.db.Exec(ctx, submit, match, user); err != nil {
			return translate(err)
		}

		const finish = `update matches set state = 'judging'
			where id = $1 and state = 'active'
			  and not exists (
				select 1 from match_players p where p.match_id = $1 and p.submitted_at is null
			  )`

		tag, err := q.db.Exec(ctx, finish, match)
		if err != nil {
			return translate(err)
		}
		judging = tag.RowsAffected() == 1

		out, err = q.matchByID(ctx, match)
		return err
	})
	if err != nil {
		return Match{}, false, err
	}
	return out, judging, nil
}

// Expire moves an active match past its deadline to judging, and reports
// whether this call was the one that moved it.
//
// A player who never submitted is simply out of time: what their workspace
// holds at the deadline is what there is to judge. The deadline is compared
// against the database's clock rather than the caller's, for the same reason
// the deadline was computed there.
//
// One statement is enough, because there is no player row to write first: the
// row lock the update takes is the whole of the serialisation.
func (q *Queries) Expire(ctx context.Context, match id.ID) (bool, error) {
	const expire = `update matches set state = 'judging'
		where id = $1 and state = 'active' and deadline_at <= now()`

	tag, err := q.db.Exec(ctx, expire, match)
	if err != nil {
		return false, translate(err)
	}
	return tag.RowsAffected() == 1, nil
}

// ExpireDue moves every match whose deadline has passed to judging, and
// returns the ones it moved. It is what the deadline finalizer calls on a
// tick, and it is the same compare and swap as [Queries.Expire] written for
// many matches at once: the guard on the state is what makes the next tick a
// no-op rather than a second transition.
//
// A match somebody is already holding is skipped rather than waited for. A
// player submitting at the deadline holds their match row for the moment that
// takes, and a tick that queued behind them would hold up every other match
// that is also due. Whoever gets there first moves the match, and skipping
// costs nothing because the next tick picks up whatever was skipped.
//
// The limit bounds how many rows one statement locks, so a backlog cannot
// turn a tick into a long write lock over the whole table.
func (q *Queries) ExpireDue(ctx context.Context, limit int) ([]id.ID, error) {
	const expire = `update matches set state = 'judging'
		where id in (
			select id from matches
			where state = 'active' and deadline_at <= now()
			order by deadline_at
			limit $1
			for update skip locked
		)
		returning id`

	rows, err := q.db.Query(ctx, expire, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var expired []id.ID
	for rows.Next() {
		var match id.ID
		if err := rows.Scan(&match); err != nil {
			return nil, fmt.Errorf("store: read expired match: %w", err)
		}
		expired = append(expired, match)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: expire due matches: %w", err)
	}
	return expired, nil
}

// seedWorkspaces gives both players the challenge's starting files.
//
// One statement, so the bytes never travel through this process: they are
// already in the database, next to the challenge version that a match points
// at. Both players are seeded from the same rows in the same transaction,
// which is what makes "you both started from the same code" a fact rather
// than a claim.
//
// A challenge whose workspace was never registered would seed nothing and
// hand two players an empty editor, so that is refused here instead.
func (q *Queries) seedWorkspaces(ctx context.Context, match id.ID) error {
	const seed = `insert into workspace_files (match_id, user_id, path, content)
		select p.match_id, p.user_id, f.path, f.content
		from match_players p
		join matches m on m.id = p.match_id
		join challenge_files f
		  on f.challenge_id = m.challenge_id and f.challenge_version = m.challenge_version
		where p.match_id = $1
		on conflict do nothing`

	tag, err := q.db.Exec(ctx, seed, match)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoStartingWorkspace
	}
	return nil
}

// lockMatchForPlayer takes the match row and then returns the match, only to
// a player seated in it. Anyone else gets [ErrNotFound], the same answer as a
// match that is not there.
func (q *Queries) lockMatchForPlayer(ctx context.Context, match, user id.ID) (Match, error) {
	const lock = `select 1 from matches where id = $1 for update`
	if _, err := q.db.Exec(ctx, lock, match); err != nil {
		return Match{}, translate(err)
	}
	return q.MatchForPlayer(ctx, match, user)
}
