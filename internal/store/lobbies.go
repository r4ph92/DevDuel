package store

import (
	"context"
	"fmt"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
)

// MatchState is where a match is in its life. The lobby queries here only
// open and cancel lobbies; every other transition belongs to the match state
// machine.
type MatchState string

// The states a match can be in, matching the check on matches.state.
const (
	MatchLobby     MatchState = "lobby"
	MatchActive    MatchState = "active"
	MatchJudging   MatchState = "judging"
	MatchComplete  MatchState = "complete"
	MatchAbandoned MatchState = "abandoned"
)

// Match is a match and the players seated in it.
//
// It carries the challenge because the store has no business deciding who
// may see it. Whether a lobby reveals its challenge is the caller's call.
type Match struct {
	ID        id.ID
	Code      string
	State     MatchState
	Challenge challenge.Key
	CreatedAt time.Time
	// Players in slot order. A match always has at least its host.
	Players []Player
}

// Player is one seat in a match.
type Player struct {
	UserID   id.ID
	Username string
	Slot     int
	JoinedAt time.Time
}

// Has reports whether user is seated in the match.
func (m Match) Has(user id.ID) bool {
	for _, p := range m.Players {
		if p.UserID == user {
			return true
		}
	}
	return false
}

// slots is how many players a match seats.
const slots = 2

// CreateLobby opens a lobby for challenge with host in the first seat.
//
// It returns [ErrLobbyCodeTaken] when code collides with an open lobby, and
// [ErrInUnfinishedMatch] when host is already in a match that has not
// finished. Both come from unique indexes rather than from a check made first,
// because a check made first is a race.
func (q *Queries) CreateLobby(ctx context.Context, host id.ID, key challenge.Key, code string) (Match, error) {
	var out Match
	err := q.InTx(ctx, func(q *Queries) error {
		match := id.New()

		const insertMatch = `insert into matches (id, challenge_id, challenge_version, lobby_code)
			values ($1, $2, $3, $4)`
		if _, err := q.db.Exec(ctx, insertMatch, match, key.ID, key.Version, code); err != nil {
			return translate(err)
		}

		const seat = `insert into match_players (match_id, user_id, slot) values ($1, $2, 1)`
		if _, err := q.db.Exec(ctx, seat, match, host); err != nil {
			return translate(err)
		}

		var err error
		out, err = q.matchByID(ctx, match)
		return err
	})
	if err != nil {
		return Match{}, err
	}
	return out, nil
}

// JoinLobby seats user in the open lobby that code names.
//
// Joining a lobby the player is already in returns it unchanged, so a client
// that lost the response can simply ask again. A code that names no open
// lobby is [ErrNotFound]: codes are recycled once a match starts, so a code
// identifies nothing after that. A full lobby is [ErrLobbyFull], and a player
// already in another unfinished match is [ErrInUnfinishedMatch].
func (q *Queries) JoinLobby(ctx context.Context, user id.ID, code string) (Match, error) {
	var out Match
	err := q.InTx(ctx, func(q *Queries) error {
		// Taking the lobby row here is what keeps the state read below from
		// going stale: a lobby cancelled while this waits no longer matches
		// once the lock is granted, and reads as not found rather than being
		// joined on its way out.
		//
		// Seat inserts are serialised whether or not this lock is taken,
		// because the trigger that derives the membership flag takes the same
		// row. That is the guarantee two simultaneous joiners actually rest
		// on, and it is tested directly.
		var match id.ID
		const find = `select id from matches where lobby_code = $1 and state = 'lobby' for update`
		if err := q.db.QueryRow(ctx, find, code).Scan(&match); err != nil {
			return translate(err)
		}

		current, err := q.matchByID(ctx, match)
		if err != nil {
			return err
		}
		if current.Has(user) {
			out = current
			return nil
		}
		slot, ok := freeSlot(current)
		if !ok {
			return ErrLobbyFull
		}

		const seat = `insert into match_players (match_id, user_id, slot) values ($1, $2, $3)`
		if _, err := q.db.Exec(ctx, seat, match, user, slot); err != nil {
			return translate(err)
		}

		out, err = q.matchByID(ctx, match)
		return err
	})
	if err != nil {
		return Match{}, err
	}
	return out, nil
}

// freeSlot is the lowest seat nobody holds.
func freeSlot(m Match) (int, bool) {
	taken := make(map[int]bool, len(m.Players))
	for _, p := range m.Players {
		taken[p.Slot] = true
	}
	for slot := 1; slot <= slots; slot++ {
		if !taken[slot] {
			return slot, true
		}
	}
	return 0, false
}

// MatchForPlayer returns the match if user is seated in it. A match the user
// is not in is [ErrNotFound], exactly as if it did not exist, so the answer
// cannot be used to find out which match ids are real.
func (q *Queries) MatchForPlayer(ctx context.Context, match, user id.ID) (Match, error) {
	m, err := q.matchByID(ctx, match)
	if err != nil {
		return Match{}, err
	}
	if !m.Has(user) {
		return Match{}, ErrNotFound
	}
	return m, nil
}

// CurrentMatch returns the unfinished match user is in, or [ErrNotFound].
// It is how a client finds its way back after a reload or a lost response.
func (q *Queries) CurrentMatch(ctx context.Context, user id.ID) (Match, error) {
	var match id.ID
	const find = `select match_id from match_players where user_id = $1 and unfinished`
	if err := q.db.QueryRow(ctx, find, user).Scan(&match); err != nil {
		return Match{}, translate(err)
	}
	return q.matchByID(ctx, match)
}

// CancelLobby abandons a lobby that has not started, releasing both players.
//
// Cancelling a lobby that is already abandoned succeeds, so a retried request
// does not turn into an error. A match that has started is [ErrMatchStarted],
// and a match the user is not in is [ErrNotFound].
func (q *Queries) CancelLobby(ctx context.Context, match, user id.ID) error {
	return q.InTx(ctx, func(q *Queries) error {
		// Locked first, so a joiner cannot slip in between the read and the
		// update, and so this takes the match before its players like every
		// other writer does.
		const lock = `select 1 from matches where id = $1 for update`
		if _, err := q.db.Exec(ctx, lock, match); err != nil {
			return translate(err)
		}

		m, err := q.MatchForPlayer(ctx, match, user)
		if err != nil {
			return err
		}

		switch m.State {
		case MatchAbandoned:
			return nil
		case MatchLobby:
		default:
			return ErrMatchStarted
		}

		const abandon = `update matches set state = 'abandoned', ended_at = now()
			where id = $1 and state = 'lobby'`
		if _, err := q.db.Exec(ctx, abandon, match); err != nil {
			return translate(err)
		}
		return nil
	})
}

// matchByID reads a match and its players in one statement, so the two
// cannot disagree about a join that landed in between.
func (q *Queries) matchByID(ctx context.Context, match id.ID) (Match, error) {
	const query = `select m.id, m.lobby_code, m.state, m.challenge_id, m.challenge_version, m.created_at,
			p.user_id, u.username, p.slot, p.joined_at
		from matches m
		join match_players p on p.match_id = m.id
		join users u on u.id = p.user_id
		where m.id = $1
		order by p.slot`

	rows, err := q.db.Query(ctx, query, match)
	if err != nil {
		return Match{}, translate(err)
	}
	defer rows.Close()

	var out Match
	for rows.Next() {
		var p Player
		err := rows.Scan(&out.ID, &out.Code, &out.State, &out.Challenge.ID, &out.Challenge.Version, &out.CreatedAt,
			&p.UserID, &p.Username, &p.Slot, &p.JoinedAt)
		if err != nil {
			return Match{}, fmt.Errorf("store: read match: %w", err)
		}
		out.Players = append(out.Players, p)
	}
	if err := rows.Err(); err != nil {
		return Match{}, fmt.Errorf("store: read match: %w", err)
	}
	if len(out.Players) == 0 {
		return Match{}, ErrNotFound
	}
	return out, nil
}
