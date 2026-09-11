package store

import (
	"context"

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
