package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// lobby is a database with a challenge registered and the players a test
// needs, which is the setup every test here shares.
type lobby struct {
	db  *store.Store
	key challenge.Key
}

func newLobbyFixture(t *testing.T) lobby {
	t.Helper()

	db := storetest.New(t)
	return lobby{db: db, key: seedChallenge(t, db)}
}

// create opens a lobby, failing the test if it cannot.
func (l lobby) create(t *testing.T, host id.ID, code string) store.Match {
	t.Helper()

	m, err := l.db.CreateLobby(t.Context(), host, l.key, code)
	if err != nil {
		t.Fatalf("create lobby %s: %v", code, err)
	}
	return m
}

func TestCreateLobbySeatsTheHost(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host := seedUser(t, l.db)

	m := l.create(t, host, "ABCD2345")

	if m.Code != "ABCD2345" {
		t.Errorf("code = %q, want ABCD2345", m.Code)
	}
	if m.State != store.MatchLobby {
		t.Errorf("state = %q, want lobby", m.State)
	}
	if m.Challenge != l.key {
		t.Errorf("challenge = %v, want %v", m.Challenge, l.key)
	}
	if len(m.Players) != 1 {
		t.Fatalf("seated %d players, want 1", len(m.Players))
	}
	if m.Players[0].UserID != host || m.Players[0].Slot != 1 {
		t.Errorf("host is %v in slot %d, want %v in slot 1", m.Players[0].UserID, m.Players[0].Slot, host)
	}
	if m.Players[0].Username == "" {
		t.Error("player has no username")
	}
}

// A code only has to be unique among lobbies somebody could still join.
func TestLobbyCodesAreUniqueWhileTheLobbyIsOpen(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	first, second := seedUser(t, l.db), seedUser(t, l.db)

	opened := l.create(t, first, "SHARED12")

	_, err := l.db.CreateLobby(t.Context(), second, l.key, "SHARED12")
	if !errors.Is(err, store.ErrLobbyCodeTaken) {
		t.Fatalf("reusing an open code = %v, want ErrLobbyCodeTaken", err)
	}

	if err := l.db.CancelLobby(t.Context(), opened.ID, first); err != nil {
		t.Fatalf("cancel lobby: %v", err)
	}
	if _, err := l.db.CreateLobby(t.Context(), second, l.key, "SHARED12"); err != nil {
		t.Errorf("reusing the code of a closed lobby: %v", err)
	}
}

func TestJoinLobbyTakesTheSecondSeat(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, guest := seedUser(t, l.db), seedUser(t, l.db)

	opened := l.create(t, host, "JOIN2345")

	joined, err := l.db.JoinLobby(t.Context(), guest, "JOIN2345")
	if err != nil {
		t.Fatalf("join lobby: %v", err)
	}
	if joined.ID != opened.ID {
		t.Errorf("joined match %v, want %v", joined.ID, opened.ID)
	}
	if len(joined.Players) != 2 {
		t.Fatalf("seated %d players, want 2", len(joined.Players))
	}
	if joined.Players[1].UserID != guest || joined.Players[1].Slot != 2 {
		t.Errorf("guest is %v in slot %d, want %v in slot 2", joined.Players[1].UserID, joined.Players[1].Slot, guest)
	}

	// The host sees the guest without having to do anything.
	seen, err := l.db.MatchForPlayer(t.Context(), opened.ID, host)
	if err != nil {
		t.Fatalf("host reads the lobby: %v", err)
	}
	if len(seen.Players) != 2 {
		t.Errorf("host sees %d players, want 2", len(seen.Players))
	}
}

// A client that lost the response asks again, and must not be told it is
// already in the lobby it is in.
func TestJoiningALobbyYouAreInIsIdempotent(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, guest := seedUser(t, l.db), seedUser(t, l.db)

	opened := l.create(t, host, "AGAIN234")
	if _, err := l.db.JoinLobby(t.Context(), guest, "AGAIN234"); err != nil {
		t.Fatalf("join lobby: %v", err)
	}

	for name, user := range map[string]id.ID{"guest": guest, "host": host} {
		t.Run(name, func(t *testing.T) {
			again, err := l.db.JoinLobby(t.Context(), user, "AGAIN234")
			if err != nil {
				t.Fatalf("join again: %v", err)
			}
			if again.ID != opened.ID || len(again.Players) != 2 {
				t.Errorf("join again gave match %v with %d players, want %v with 2",
					again.ID, len(again.Players), opened.ID)
			}
		})
	}
}

func TestJoinLobbyRefusesAThirdPlayerAndAnUnknownCode(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, guest, third := seedUser(t, l.db), seedUser(t, l.db), seedUser(t, l.db)

	l.create(t, host, "FULL2345")
	if _, err := l.db.JoinLobby(t.Context(), guest, "FULL2345"); err != nil {
		t.Fatalf("join lobby: %v", err)
	}

	if _, err := l.db.JoinLobby(t.Context(), third, "FULL2345"); !errors.Is(err, store.ErrLobbyFull) {
		t.Errorf("third player = %v, want ErrLobbyFull", err)
	}
	if _, err := l.db.JoinLobby(t.Context(), third, "NOSUCH23"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown code = %v, want ErrNotFound", err)
	}
}

func TestAPlayerCanOnlyBeInOneUnfinishedMatch(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, other := seedUser(t, l.db), seedUser(t, l.db)

	l.create(t, host, "FIRST234")
	l.create(t, other, "OTHER234")

	if _, err := l.db.CreateLobby(t.Context(), host, l.key, "SECOND23"); !errors.Is(err, store.ErrInUnfinishedMatch) {
		t.Errorf("second lobby = %v, want ErrInUnfinishedMatch", err)
	}
	if _, err := l.db.JoinLobby(t.Context(), host, "OTHER234"); !errors.Is(err, store.ErrInUnfinishedMatch) {
		t.Errorf("joining while in a match = %v, want ErrInUnfinishedMatch", err)
	}

	// The refused lobby left nothing behind.
	var open int
	const count = `select count(*) from matches where lobby_code = 'SECOND23'`
	if err := l.db.Pool().QueryRow(t.Context(), count).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("%d matches survived a refused create, want 0", open)
	}
}

// Two people pasting the same code at once. The lobby has one free seat, so
// exactly one of them gets it and the rest are told it is full.
func TestConcurrentJoinersGetOneSeat(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host := seedUser(t, l.db)
	l.create(t, host, "RACE2345")

	const joiners = 8
	guests := make([]id.ID, joiners)
	for i := range guests {
		guests[i] = seedUser(t, l.db)
	}

	errs := make(chan error, joiners)
	start := make(chan struct{})
	for _, guest := range guests {
		go func() {
			<-start
			_, err := l.db.JoinLobby(context.WithoutCancel(t.Context()), guest, "RACE2345")
			errs <- err
		}()
	}
	close(start)

	var seated int
	for range joiners {
		switch err := <-errs; {
		case err == nil:
			seated++
		case errors.Is(err, store.ErrLobbyFull):
		default:
			t.Errorf("join: %v", err)
		}
	}
	if seated != 1 {
		t.Errorf("%d joiners were seated, want 1", seated)
	}

	final, err := l.db.MatchForPlayer(t.Context(), l.currentID(t, host), host)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Players) != 2 {
		t.Errorf("lobby holds %d players, want 2", len(final.Players))
	}
}

// The same player opening a lobby and accepting an invitation at the same
// instant. One of the two has to lose, or they are in two matches at once.
func TestConcurrentCreateAndJoinLeaveOneMembership(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	player, friend := seedUser(t, l.db), seedUser(t, l.db)
	l.create(t, friend, "INVITE23")

	ctx := context.WithoutCancel(t.Context())
	errs := make(chan error, 2)
	start := make(chan struct{})
	go func() {
		<-start
		_, err := l.db.CreateLobby(ctx, player, l.key, "OWNLOB23")
		errs <- err
	}()
	go func() {
		<-start
		_, err := l.db.JoinLobby(ctx, player, "INVITE23")
		errs <- err
	}()
	close(start)

	var won int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			won++
		case errors.Is(err, store.ErrInUnfinishedMatch):
		default:
			t.Errorf("concurrent create and join: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of the two attempts succeeded, want 1", won)
	}

	var memberships int
	const count = `select count(*) from match_players where user_id = $1 and unfinished`
	if err := l.db.Pool().QueryRow(t.Context(), count, player).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Errorf("player holds %d unfinished memberships, want 1", memberships)
	}
}

func TestCancelLobbyReleasesBothPlayers(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, guest := seedUser(t, l.db), seedUser(t, l.db)

	opened := l.create(t, host, "LEAVE234")
	if _, err := l.db.JoinLobby(t.Context(), guest, "LEAVE234"); err != nil {
		t.Fatalf("join lobby: %v", err)
	}

	// The guest leaving cancels the lobby, rather than freeing a seat.
	if err := l.db.CancelLobby(t.Context(), opened.ID, guest); err != nil {
		t.Fatalf("cancel lobby: %v", err)
	}

	cancelled, err := l.db.MatchForPlayer(t.Context(), opened.ID, host)
	if err != nil {
		t.Fatalf("read cancelled lobby: %v", err)
	}
	if cancelled.State != store.MatchAbandoned {
		t.Errorf("state = %q, want abandoned", cancelled.State)
	}

	// Both are free to play again, and the dead code opens nothing.
	if _, err := l.db.CreateLobby(t.Context(), host, l.key, "HOSTNEW2"); err != nil {
		t.Errorf("host opens another lobby: %v", err)
	}
	if _, err := l.db.CreateLobby(t.Context(), guest, l.key, "GUESTNE2"); err != nil {
		t.Errorf("guest opens another lobby: %v", err)
	}
	if _, err := l.db.JoinLobby(t.Context(), seedUser(t, l.db), "LEAVE234"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("joining a cancelled lobby = %v, want ErrNotFound", err)
	}
}

func TestCancelLobbyIsIdempotentAndParticipantsOnly(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, stranger := seedUser(t, l.db), seedUser(t, l.db)
	opened := l.create(t, host, "TWICE234")

	for range 2 {
		if err := l.db.CancelLobby(t.Context(), opened.ID, host); err != nil {
			t.Fatalf("cancel lobby: %v", err)
		}
	}
	if err := l.db.CancelLobby(t.Context(), opened.ID, stranger); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stranger cancelling = %v, want ErrNotFound", err)
	}
	if err := l.db.CancelLobby(t.Context(), id.New(), host); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cancelling a match that does not exist = %v, want ErrNotFound", err)
	}
}

// Cancelling is a lobby operation. Once the clock is running, ending a match
// is the state machine's business.
func TestCancelLobbyRefusesAStartedMatch(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host := seedUser(t, l.db)
	opened := l.create(t, host, "START234")

	const start = `update matches set state = 'active', started_at = now(), deadline_at = now() + interval '45 minutes'
		where id = $1`
	if _, err := l.db.Pool().Exec(t.Context(), start, opened.ID); err != nil {
		t.Fatalf("start match: %v", err)
	}

	if err := l.db.CancelLobby(t.Context(), opened.ID, host); !errors.Is(err, store.ErrMatchStarted) {
		t.Errorf("cancelling a started match = %v, want ErrMatchStarted", err)
	}
}

func TestMatchForPlayerHidesAMatchFromStrangers(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, stranger := seedUser(t, l.db), seedUser(t, l.db)
	opened := l.create(t, host, "HIDDEN23")

	if _, err := l.db.MatchForPlayer(t.Context(), opened.ID, stranger); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stranger reading a match = %v, want ErrNotFound", err)
	}
	if _, err := l.db.MatchForPlayer(t.Context(), id.New(), host); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("reading a match that does not exist = %v, want ErrNotFound", err)
	}
}

func TestCurrentMatchFollowsTheUnfinishedMembership(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host := seedUser(t, l.db)

	if _, err := l.db.CurrentMatch(t.Context(), host); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("current match of a player in none = %v, want ErrNotFound", err)
	}

	opened := l.create(t, host, "CURRENT2")
	current, err := l.db.CurrentMatch(t.Context(), host)
	if err != nil {
		t.Fatalf("current match: %v", err)
	}
	if current.ID != opened.ID {
		t.Errorf("current match = %v, want %v", current.ID, opened.ID)
	}

	if err := l.db.CancelLobby(t.Context(), opened.ID, host); err != nil {
		t.Fatalf("cancel lobby: %v", err)
	}
	if _, err := l.db.CurrentMatch(t.Context(), host); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("current match after cancelling = %v, want ErrNotFound", err)
	}
}

// currentID is the id of the user's unfinished match.
func (l lobby) currentID(t *testing.T, user id.ID) id.ID {
	t.Helper()

	m, err := l.db.CurrentMatch(t.Context(), user)
	if err != nil {
		t.Fatalf("current match: %v", err)
	}
	return m.ID
}

// The membership flag is derived from the match, so writing it directly
// changes nothing. Otherwise the one-match invariant would be advice.
func TestUnfinishedMembershipCannotBeWrittenByHand(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host := seedUser(t, l.db)
	opened := l.create(t, host, "DERIVE23")

	const forge = `update match_players set unfinished = false where match_id = $1`
	if _, err := l.db.Pool().Exec(t.Context(), forge, opened.ID); err != nil {
		t.Fatalf("update membership: %v", err)
	}

	var unfinished bool
	const read = `select unfinished from match_players where match_id = $1 and user_id = $2`
	if err := l.db.Pool().QueryRow(t.Context(), read, opened.ID, host).Scan(&unfinished); err != nil {
		t.Fatal(err)
	}
	if !unfinished {
		t.Error("membership was cleared by hand, want it recomputed from the match")
	}
	if _, err := l.db.CreateLobby(t.Context(), host, l.key, "FORGED23"); !errors.Is(err, store.ErrInUnfinishedMatch) {
		t.Errorf("second lobby after forging the flag = %v, want ErrInUnfinishedMatch", err)
	}
}

// Seating a player takes the match row, which is what actually serialises two
// people pasting the same code at the same instant: the second insert cannot
// land while the first is still filling the lobby. The lock is taken by the
// trigger that derives the membership flag, so it holds however a seat is
// inserted, not only through JoinLobby.
func TestSeatingAPlayerWaitsForTheMatch(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host, guest := seedUser(t, l.db), seedUser(t, l.db)
	opened := l.create(t, host, "SEATLK23")

	ctx := t.Context()
	holder, err := l.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := holder.Exec(ctx, `select 1 from matches where id = $1 for update`, opened.ID); err != nil {
		t.Fatalf("lock the match: %v", err)
	}

	joiner, err := l.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = joiner.Rollback(context.WithoutCancel(ctx)) }()

	// Rather than waiting for the holder, which in this test never lets go.
	if _, err := joiner.Exec(ctx, `set local lock_timeout = '1s'`); err != nil {
		t.Fatal(err)
	}

	const seat = `insert into match_players (match_id, user_id, slot) values ($1, $2, 2)`
	_, err = joiner.Exec(ctx, seat, opened.ID, guest)
	if err == nil {
		t.Fatal("seated a player while the match row was held, want the insert to wait")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != lockNotAvailable {
		t.Errorf("seating failed with %v, want a lock timeout", err)
	}
}

// lockNotAvailable is the SQLSTATE a statement gets when lock_timeout expires.
const lockNotAvailable = "55P03"

// Recording a submission must not reach for the match row. A submit and the
// deadline finalizer run at the same moment by design, and if the submit took
// the player row and then the match while the finalizer took them the other
// way round, the pair would deadlock.
func TestRecordingASubmissionDoesNotWaitOnTheMatch(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host := seedUser(t, l.db)
	opened := l.create(t, host, "SUBMIT23")

	ctx := t.Context()
	holder, err := l.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()

	// Whoever is transitioning the match holds its row for the whole
	// transaction, which is what the submit below must not need.
	if _, err := holder.Exec(ctx, `select 1 from matches where id = $1 for update`, opened.ID); err != nil {
		t.Fatalf("lock the match: %v", err)
	}

	submit, err := l.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = submit.Rollback(context.WithoutCancel(ctx)) }()

	// Rather than hanging the test for its full timeout if this regresses.
	if _, err := submit.Exec(ctx, `set local lock_timeout = '5s'`); err != nil {
		t.Fatal(err)
	}
	const record = `update match_players set submitted_at = now() where match_id = $1 and user_id = $2`
	if _, err := submit.Exec(ctx, record, opened.ID, host); err != nil {
		t.Fatalf("record a submission while the match row is held: %v", err)
	}
	if err := submit.Commit(ctx); err != nil {
		t.Fatalf("commit the submission: %v", err)
	}

	var submitted *time.Time
	const read = `select submitted_at from match_players where match_id = $1 and user_id = $2`
	if err := l.db.Pool().QueryRow(ctx, read, opened.ID, host).Scan(&submitted); err != nil {
		t.Fatal(err)
	}
	if submitted == nil {
		t.Error("submission was not recorded")
	}
}
