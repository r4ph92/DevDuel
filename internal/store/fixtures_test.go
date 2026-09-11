package store_test

import (
	"testing"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
)

// The fixtures the tests in this package share. Everything here is written in
// raw SQL rather than through the typed queries: a schema test that leaned on
// the query layer would stop being a test of the schema, and the queries for
// matches and challenges do not exist yet anyway.

// match is one seeded match with two players, enough to hang everything else
// off.
type match struct {
	id        id.ID
	players   [2]id.ID
	challenge string
	version   int
}

func seed(t *testing.T, db *store.Store) match {
	t.Helper()
	ctx := t.Context()

	key := seedChallenge(t, db)
	m := match{id: id.New(), challenge: key.ID, version: key.Version}

	const insertMatch = `insert into matches (id, challenge_id, challenge_version, lobby_code)
		values ($1, $2, $3, $4)`
	if _, err := db.Pool().Exec(ctx, insertMatch, m.id, m.challenge, m.version, "LOBBY1"); err != nil {
		t.Fatalf("insert match: %v", err)
	}

	for i := range m.players {
		m.players[i] = seedUser(t, db)

		const join = `insert into match_players (match_id, user_id, slot) values ($1, $2, $3)`
		if _, err := db.Pool().Exec(ctx, join, m.id, m.players[i], i+1); err != nil {
			t.Fatalf("insert match player: %v", err)
		}
	}
	return m
}

// seedChallenge registers a one-requirement challenge for matches to point at.
func seedChallenge(t *testing.T, db *store.Store) challenge.Key {
	t.Helper()
	ctx := t.Context()

	key := challenge.Key{ID: "todo-api", Version: 1}

	const insertChallenge = `insert into challenges
		(id, version, category, difficulty, duration, image_tag)
		values ($1, $2, 'debugging', 'medium', '45 minutes', 'devduel/todo-api:1')`
	if _, err := db.Pool().Exec(ctx, insertChallenge, key.ID, key.Version); err != nil {
		t.Fatalf("insert challenge: %v", err)
	}

	const insertRequirement = `insert into requirements
		(challenge_id, challenge_version, key, position, title, description, weight, broken)
		values ($1, $2, 'health', 0, 'GET /health answers', 'it answers', 1, false)`
	if _, err := db.Pool().Exec(ctx, insertRequirement, key.ID, key.Version); err != nil {
		t.Fatalf("insert requirement: %v", err)
	}

	// A starting workspace, because a match cannot start without one: the
	// transition that starts the clock seeds both players from these rows.
	const insertFile = `insert into challenge_files (challenge_id, challenge_version, path, content)
		values ($1, $2, 'server.js', 'console.log("hello")'),
		       ($1, $2, 'src/todos.js', 'module.exports = {}')`
	if _, err := db.Pool().Exec(ctx, insertFile, key.ID, key.Version); err != nil {
		t.Fatalf("insert challenge file: %v", err)
	}
	return key
}

func seedUser(t *testing.T, db *store.Store) id.ID {
	t.Helper()

	user := id.New()
	const insert = `insert into users (id, email, username, password_hash)
		values ($1, $2, $3, 'argon2id$placeholder')`
	// The tail of the id, because a username is capped at 32 characters and
	// the head of a version 7 uuid is the same for every id minted this hour.
	name := "player-" + user.String()[24:]
	if _, err := db.Pool().Exec(t.Context(), insert, user, name+"@example.test", name); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return user
}
