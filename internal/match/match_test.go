package match_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/match"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// newService returns a service over a database with the repository's own
// challenge registered, since a lobby cannot point at a challenge that was
// never registered.
func newService(t *testing.T) (*match.Service, *store.Store) {
	t.Helper()

	db := storetest.New(t)
	spec, err := challenge.Load("../../challenges/todo-api")
	if err != nil {
		t.Fatalf("load challenge: %v", err)
	}
	if err := db.RegisterChallenge(t.Context(), spec); err != nil {
		t.Fatalf("register challenge: %v", err)
	}
	return match.NewService(db, []*challenge.Spec{spec}), db
}

// newPlayer inserts an account directly, because who a player is and how they
// signed up is not this package's business.
func newPlayer(t *testing.T, db *store.Store) id.ID {
	t.Helper()

	user := id.New()
	name := "player-" + user.String()[24:]
	const insert = `insert into users (id, email, username, password_hash)
		values ($1, $2, $3, 'argon2id$placeholder')`
	if _, err := db.Pool().Exec(t.Context(), insert, user, name+"@example.test", name); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return user
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func TestCreateOpensALobbyWithAShareableCode(t *testing.T) {
	t.Parallel()
	svc, db := newService(t)
	host := newPlayer(t, db)

	lobby, err := svc.Create(t.Context(), host)
	if err != nil {
		t.Fatalf("create lobby: %v", err)
	}

	if lobby.State != store.MatchLobby {
		t.Errorf("state = %q, want lobby", lobby.State)
	}
	if len(lobby.Code) != 8 {
		t.Errorf("code %q is %d characters, want 8", lobby.Code, len(lobby.Code))
	}
	for _, c := range lobby.Code {
		if !strings.ContainsRune(codeAlphabet, c) {
			t.Errorf("code %q contains %q, which is not in the alphabet", lobby.Code, c)
		}
	}
	if len(lobby.Players) != 1 || lobby.Players[0].UserID != host {
		t.Errorf("lobby holds %d players, want the host alone", len(lobby.Players))
	}
}

// Two lobbies opened back to back must not share a code, which is the part of
// drawing one the retry loop exists for.
func TestCreateDrawsADifferentCodeEachTime(t *testing.T) {
	t.Parallel()
	svc, db := newService(t)

	seen := make(map[string]bool, 10)
	for range 10 {
		lobby, err := svc.Create(t.Context(), newPlayer(t, db))
		if err != nil {
			t.Fatalf("create lobby: %v", err)
		}
		if seen[lobby.Code] {
			t.Fatalf("code %q was drawn twice", lobby.Code)
		}
		seen[lobby.Code] = true
	}
}

func TestCreateNeedsAChallenge(t *testing.T) {
	t.Parallel()
	_, db := newService(t)
	empty := match.NewService(db, nil)

	if _, err := empty.Create(t.Context(), newPlayer(t, db)); !errors.Is(err, match.ErrNoChallenges) {
		t.Errorf("create with an empty catalog = %v, want ErrNoChallenges", err)
	}
}

// A code is read off somebody's screen and pasted, so it arrives in whatever
// case and padding the reader's client produced.
func TestJoinAcceptsACodeAsItWasRead(t *testing.T) {
	t.Parallel()
	svc, db := newService(t)

	// Each case gets its own lobby, so that what one case does to a lobby
	// cannot decide whether the next one can still find its code.
	for name, asRead := range map[string]func(string) string{
		"lower case": strings.ToLower,
		"padded":     func(code string) string { return "  " + code + "\n" },
		"both":       func(code string) string { return " " + strings.ToLower(code) + " " },
	} {
		t.Run(name, func(t *testing.T) {
			lobby, err := svc.Create(t.Context(), newPlayer(t, db))
			if err != nil {
				t.Fatalf("create lobby: %v", err)
			}

			joined, err := svc.Join(t.Context(), newPlayer(t, db), asRead(lobby.Code))
			if err != nil {
				t.Fatalf("join with a %s code: %v", name, err)
			}
			if joined.ID != lobby.ID {
				t.Errorf("joined %v, want %v", joined.ID, lobby.ID)
			}
		})
	}
}

// A code that cannot name a lobby is refused before the database is asked,
// so a mistyped code is not a query.
func TestJoinRefusesAMalformedCode(t *testing.T) {
	t.Parallel()
	svc, db := newService(t)
	player := newPlayer(t, db)

	for name, code := range map[string]string{
		"empty":            "",
		"too short":        "ABC234",
		"too long":         "ABCD23456",
		"punctuation":      "ABCD234!",
		"excluded letter":  "ABCD234I",
		"excluded digit":   "ABCD2340",
		"inner whitespace": "ABCD 234",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Join(t.Context(), player, code); !errors.Is(err, match.ErrInvalidCode) {
				t.Errorf("join %q = %v, want ErrInvalidCode", code, err)
			}
		})
	}
}

func TestGetAndCurrentAnswerOnlyToPlayers(t *testing.T) {
	t.Parallel()
	svc, db := newService(t)
	host, stranger := newPlayer(t, db), newPlayer(t, db)

	lobby, err := svc.Create(t.Context(), host)
	if err != nil {
		t.Fatalf("create lobby: %v", err)
	}

	if _, err := svc.Get(t.Context(), lobby.ID, host); err != nil {
		t.Errorf("host reads its own lobby: %v", err)
	}
	if _, err := svc.Get(t.Context(), lobby.ID, stranger); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stranger reads the lobby = %v, want ErrNotFound", err)
	}

	current, err := svc.Current(t.Context(), host)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if current.ID != lobby.ID {
		t.Errorf("current = %v, want %v", current.ID, lobby.ID)
	}
	if _, err := svc.Current(t.Context(), stranger); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("current of a player in no match = %v, want ErrNotFound", err)
	}
}

// The service is a thin seam over the transitions, so this checks that the
// seam is wired, not the transitions themselves, which have their own tests.
func TestTheLifecycleRunsThroughTheService(t *testing.T) {
	t.Parallel()
	svc, db := newService(t)
	host, guest := newPlayer(t, db), newPlayer(t, db)

	lobby, err := svc.Create(t.Context(), host)
	if err != nil {
		t.Fatalf("create lobby: %v", err)
	}
	if _, err := svc.Join(t.Context(), guest, lobby.Code); err != nil {
		t.Fatalf("join lobby: %v", err)
	}

	if _, started, err := svc.Ready(t.Context(), lobby.ID, host); err != nil || started {
		t.Fatalf("host readies: started=%v err=%v", started, err)
	}
	running, started, err := svc.Ready(t.Context(), lobby.ID, guest)
	if err != nil || !started {
		t.Fatalf("guest readies: started=%v err=%v", started, err)
	}
	if running.State != store.MatchActive {
		t.Fatalf("state = %q, want active", running.State)
	}

	if _, judging, err := svc.Submit(t.Context(), lobby.ID, host); err != nil || judging {
		t.Fatalf("host submits: judging=%v err=%v", judging, err)
	}
	done, judging, err := svc.Submit(t.Context(), lobby.ID, guest)
	if err != nil || !judging {
		t.Fatalf("guest submits: judging=%v err=%v", judging, err)
	}
	if done.State != store.MatchJudging {
		t.Errorf("state = %q, want judging", done.State)
	}

	// Expiring answers to the finalizer, and a match already past judging is
	// nothing for it to move.
	if moved, err := svc.Expire(t.Context(), lobby.ID); err != nil || moved {
		t.Errorf("expiring a finished match: moved=%v err=%v", moved, err)
	}
}

func TestLeaveReleasesBothPlayers(t *testing.T) {
	t.Parallel()
	svc, db := newService(t)
	host, guest := newPlayer(t, db), newPlayer(t, db)

	lobby, err := svc.Create(t.Context(), host)
	if err != nil {
		t.Fatalf("create lobby: %v", err)
	}
	if _, err := svc.Join(t.Context(), guest, lobby.Code); err != nil {
		t.Fatalf("join lobby: %v", err)
	}

	if err := svc.Leave(t.Context(), lobby.ID, host); err != nil {
		t.Fatalf("leave: %v", err)
	}

	for name, player := range map[string]id.ID{"host": host, "guest": guest} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Create(t.Context(), player); err != nil {
				t.Errorf("%s opens a lobby after leaving: %v", name, err)
			}
		})
	}
	if _, err := svc.Join(t.Context(), newPlayer(t, db), lobby.Code); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("joining the cancelled lobby = %v, want ErrNotFound", err)
	}
}
