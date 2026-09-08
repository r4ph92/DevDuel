package store_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// These test the schema, not a query layer: every assertion here is about a
// constraint, an index or a trigger holding when the Go code that is supposed
// to uphold it does the wrong thing. That is the whole point of putting the
// invariants in the database.

// match is one seeded match with two players, enough to hang everything else
// off.
type match struct {
	id        id.ID
	players   [2]id.ID
	challenge string
	version   int
}

func seed(t *testing.T, db *pgxpool.Pool) match {
	t.Helper()
	ctx := t.Context()

	m := match{id: id.New(), challenge: "todo-api", version: 1}

	const insertChallenge = `insert into challenges
		(id, version, category, difficulty, duration, image_tag)
		values ($1, $2, 'debugging', 'medium', '45 minutes', 'devduel/todo-api:1')`
	if _, err := db.Exec(ctx, insertChallenge, m.challenge, m.version); err != nil {
		t.Fatalf("insert challenge: %v", err)
	}

	const insertRequirement = `insert into requirements
		(challenge_id, challenge_version, key, position, title, description, weight, broken)
		values ($1, $2, 'health', 0, 'GET /health answers', 'it answers', 1, false)`
	if _, err := db.Exec(ctx, insertRequirement, m.challenge, m.version); err != nil {
		t.Fatalf("insert requirement: %v", err)
	}

	const insertMatch = `insert into matches (id, challenge_id, challenge_version, lobby_code)
		values ($1, $2, $3, $4)`
	if _, err := db.Exec(ctx, insertMatch, m.id, m.challenge, m.version, "LOBBY1"); err != nil {
		t.Fatalf("insert match: %v", err)
	}

	for i := range m.players {
		m.players[i] = seedUser(t, db)

		const join = `insert into match_players (match_id, user_id, slot) values ($1, $2, $3)`
		if _, err := db.Exec(ctx, join, m.id, m.players[i], i+1); err != nil {
			t.Fatalf("insert match player: %v", err)
		}
	}
	return m
}

func seedUser(t *testing.T, db *pgxpool.Pool) id.ID {
	t.Helper()

	user := id.New()
	const insert = `insert into users (id, email, username, password_hash)
		values ($1, $2, $3, 'argon2id$placeholder')`
	// The tail of the id, because a username is capped at 32 characters and
	// the head of a version 7 uuid is the same for every id minted this hour.
	name := "player-" + user.String()[24:]
	if _, err := db.Exec(t.Context(), insert, user, name+"@example.test", name); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return user
}

// appendEvent is how every writer must append: bump the match's allocator and
// insert the event in one transaction. Nothing else produces a usable seq.
func appendEvent(ctx context.Context, tx pgx.Tx, matchID id.ID, kind string) (int64, error) {
	var seq int64
	const allocate = `update matches set event_seq = event_seq + 1 where id = $1 returning event_seq`
	if err := tx.QueryRow(ctx, allocate, matchID).Scan(&seq); err != nil {
		return 0, err
	}

	const insert = `insert into match_events (match_id, seq, type) values ($1, $2, $3)`
	if _, err := tx.Exec(ctx, insert, matchID, seq, kind); err != nil {
		return 0, err
	}
	return seq, nil
}

// Resume is "everything after seq N", which is only sound if an event that
// was delivered can never change or vanish.
func TestMatchEventsCannotBeEditedOrRemoved(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := appendEvent(ctx, tx, m.id, "match.started"); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	for name, stmt := range map[string]string{
		"update": `update match_events set type = 'match.ended' where match_id = $1`,
		"delete": `delete from match_events where match_id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(ctx, stmt, m.id); err == nil {
				t.Errorf("%s on match_events succeeded, want a refusal", name)
			}
		})
	}

	// And deleting the match cannot be used to get around the trigger.
	if _, err := db.Exec(ctx, `delete from matches where id = $1`, m.id); err == nil {
		t.Error("deleting a match with events succeeded, want a refusal")
	}
}

// Two writers appending at once must produce 1 and 2, not 1 and 1, and not
// 1 and 3. A hole would make a reconnecting client wait forever for an event
// that was never written.
func TestMatchEventSequenceIsGaplessUnderConcurrency(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	const writers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seqs []int64
	)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			tx, err := db.Begin(ctx)
			if err != nil {
				t.Errorf("begin: %v", err)
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()

			seq, err := appendEvent(ctx, tx, m.id, "judge.finished")
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				t.Errorf("writer %d commit: %v", i, err)
				return
			}

			mu.Lock()
			defer mu.Unlock()
			seqs = append(seqs, seq)
		}()
	}
	wg.Wait()

	if len(seqs) != writers {
		t.Fatalf("%d writers appended, want %d", len(seqs), writers)
	}

	seen := make(map[int64]bool, len(seqs))
	for _, seq := range seqs {
		if seq < 1 || seq > writers {
			t.Errorf("seq %d is outside 1..%d", seq, writers)
		}
		if seen[seq] {
			t.Errorf("seq %d was handed out twice", seq)
		}
		seen[seq] = true
	}
}

// The judge queue depends on this: a player leaning on the Run button must
// not be able to stack up runs.
func TestOnlyOneJudgeJobPerPlayerIsInFlight(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	const queue = `insert into judge_jobs (id, match_id, user_id) values ($1, $2, $3)`
	if _, err := db.Exec(ctx, queue, id.New(), m.id, m.players[0]); err != nil {
		t.Fatalf("queue first job: %v", err)
	}

	if _, err := db.Exec(ctx, queue, id.New(), m.id, m.players[0]); err == nil {
		t.Error("queued a second job while one was in flight, want a refusal")
	}

	// The other player is unaffected.
	if _, err := db.Exec(ctx, queue, id.New(), m.id, m.players[1]); err != nil {
		t.Errorf("queue the other player's job: %v", err)
	}

	// Once the first job is out of flight, that player can run again.
	const finish = `update judge_jobs set state = 'succeeded', started_at = now(), finished_at = now()
		where match_id = $1 and user_id = $2`
	if _, err := db.Exec(ctx, finish, m.id, m.players[0]); err != nil {
		t.Fatalf("finish first job: %v", err)
	}
	if _, err := db.Exec(ctx, queue, id.New(), m.id, m.players[0]); err != nil {
		t.Errorf("queue a job after the previous one finished: %v", err)
	}
}

// Both players submitting and the deadline firing will race. The guarded
// update is what makes the loser of that race a no-op rather than a second
// transition.
func TestStartingAMatchIsIdempotent(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	// The deadline is computed in SQL from the challenge's duration, so no
	// caller ever gets to decide how long a match lasts.
	const start = `update matches m
		set state = 'active', started_at = now(), deadline_at = now() + c.duration
		from challenges c
		where m.id = $1
		  and c.id = m.challenge_id and c.version = m.challenge_version
		  and m.state = 'lobby'`

	first, err := db.Exec(ctx, start, m.id)
	if err != nil {
		t.Fatalf("start match: %v", err)
	}
	if first.RowsAffected() != 1 {
		t.Fatalf("first start affected %d rows, want 1", first.RowsAffected())
	}

	second, err := db.Exec(ctx, start, m.id)
	if err != nil {
		t.Fatalf("start match again: %v", err)
	}
	if second.RowsAffected() != 0 {
		t.Errorf("second start affected %d rows, want 0", second.RowsAffected())
	}

	var minutes float64
	const clock = `select extract(epoch from (deadline_at - started_at)) / 60 from matches where id = $1`
	if err := db.QueryRow(ctx, clock, m.id).Scan(&minutes); err != nil {
		t.Fatalf("read the clock: %v", err)
	}
	if minutes != 45 {
		t.Errorf("match runs for %v minutes, want the challenge's 45", minutes)
	}
}

func TestChallengeVersionsAreImmutable(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	for name, stmt := range map[string]string{
		"the challenge":   `update challenges set difficulty = 'hard' where id = $1`,
		"its requirement": `update requirements set weight = 99 where challenge_id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(ctx, stmt, m.challenge); err == nil {
				t.Errorf("editing %s succeeded, want a refusal", name)
			}
		})
	}

	// A played version cannot be deleted either, so history stays explicable.
	if _, err := db.Exec(ctx, `delete from challenges where id = $1`, m.challenge); err == nil {
		t.Error("deleting a played challenge succeeded, want a refusal")
	}
}

func TestTheWinnerMustHavePlayedTheMatch(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)
	stranger := seedUser(t, db)

	const declare = `update matches set winner_user_id = $2 where id = $1`
	if _, err := db.Exec(ctx, declare, m.id, stranger); err == nil {
		t.Error("recorded a winner who was not in the match, want a refusal")
	}
	if _, err := db.Exec(ctx, declare, m.id, m.players[1]); err != nil {
		t.Fatalf("record a player as the winner: %v", err)
	}

	// Reading an id back out again, which is the other half of storing ids
	// as uuid rather than as text.
	var winner id.ID
	const read = `select winner_user_id from matches where id = $1`
	if err := db.QueryRow(ctx, read, m.id).Scan(&winner); err != nil {
		t.Fatalf("read the winner: %v", err)
	}
	if winner != m.players[1] {
		t.Errorf("winner = %s, want %s", winner, m.players[1])
	}
}

// A lobby that nobody ever joined still has to be closable, and it never had
// a clock to close.
func TestAnUnstartedLobbyCanBeAbandoned(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	const abandon = `update matches set state = 'abandoned', ended_at = now()
		where id = $1 and state = 'lobby'`
	res, err := db.Exec(ctx, abandon, m.id)
	if err != nil {
		t.Fatalf("abandon lobby: %v", err)
	}
	if res.RowsAffected() != 1 {
		t.Errorf("abandoning affected %d rows, want 1", res.RowsAffected())
	}
}

func TestWorkspacePathsCannotEscapeTheWorkspace(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	const write = `insert into workspace_files (match_id, user_id, path, content)
		values ($1, $2, $3, $4)`

	if _, err := db.Exec(ctx, write, m.id, m.players[0], "src/server.js", []byte("ok")); err != nil {
		t.Fatalf("write a normal file: %v", err)
	}

	for name, path := range map[string]string{
		"absolute":       "/etc/passwd",
		"parent":         "../secrets",
		"parent within":  "src/../../secrets",
		"current":        "./server.js",
		"empty":          "",
		"trailing slash": "src/",
		"doubled slash":  "src//server.js",
		"too long":       strings.Repeat("a", 513),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(ctx, write, m.id, m.players[0], path, []byte("x")); err == nil {
				t.Errorf("wrote %q, want a refusal", path)
			}
		})
	}
}

// The workspace belongs to the player's seat in the match and nothing else,
// so it goes when the seat goes.
func TestAWorkspaceGoesWithItsPlayer(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	const write = `insert into workspace_files (match_id, user_id, path, content)
		values ($1, $2, 'server.js', 'x')`
	if _, err := db.Exec(ctx, write, m.id, m.players[0]); err != nil {
		t.Fatalf("write file: %v", err)
	}

	const leave = `delete from match_players where match_id = $1 and user_id = $2`
	if _, err := db.Exec(ctx, leave, m.id, m.players[0]); err != nil {
		t.Fatalf("remove player: %v", err)
	}

	var left int
	const count = `select count(*) from workspace_files where match_id = $1 and user_id = $2`
	if err := db.QueryRow(ctx, count, m.id, m.players[0]).Scan(&left); err != nil {
		t.Fatalf("count files: %v", err)
	}
	if left != 0 {
		t.Errorf("%d workspace files outlived their player, want 0", left)
	}
}
