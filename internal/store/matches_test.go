package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
)

// filled is a lobby with both seats taken, which is where every test here
// starts.
func (l lobby) filled(t *testing.T, code string) (match id.ID, host, guest id.ID) {
	t.Helper()

	host, guest = seedUser(t, l.db), seedUser(t, l.db)
	opened := l.create(t, host, code)
	if _, err := l.db.JoinLobby(t.Context(), guest, code); err != nil {
		t.Fatalf("join lobby: %v", err)
	}
	return opened.ID, host, guest
}

// started is a match with both players ready and the clock running.
func (l lobby) started(t *testing.T, code string) (match id.ID, host, guest id.ID) {
	t.Helper()

	match, host, guest = l.filled(t, code)
	if _, _, err := l.db.ReadyUp(t.Context(), match, host); err != nil {
		t.Fatalf("host readies: %v", err)
	}
	if _, _, err := l.db.ReadyUp(t.Context(), match, guest); err != nil {
		t.Fatalf("guest readies: %v", err)
	}
	return match, host, guest
}

func TestTheSecondReadyStartsTheMatch(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, guest := l.filled(t, "READY234")

	waiting, started, err := l.db.ReadyUp(t.Context(), match, host)
	if err != nil {
		t.Fatalf("host readies: %v", err)
	}
	if started {
		t.Error("one ready started the match, want it to wait for the second")
	}
	if waiting.State != store.MatchLobby {
		t.Errorf("state after one ready = %q, want lobby", waiting.State)
	}
	if waiting.StartedAt != nil || waiting.DeadlineAt != nil {
		t.Error("the clock is running before the match started")
	}
	if waiting.Players[0].ReadyAt == nil {
		t.Error("the host who readied is not marked ready")
	}

	running, started, err := l.db.ReadyUp(t.Context(), match, guest)
	if err != nil {
		t.Fatalf("guest readies: %v", err)
	}
	if !started {
		t.Error("the second ready did not report starting the match")
	}
	if running.State != store.MatchActive {
		t.Errorf("state = %q, want active", running.State)
	}
	if running.StartedAt == nil || running.DeadlineAt == nil {
		t.Fatal("the match started without a clock")
	}

	// The clock comes from the challenge, so that no caller decides how long
	// a match lasts.
	if got := running.DeadlineAt.Sub(*running.StartedAt); got != 45*time.Minute {
		t.Errorf("match runs for %v, want the challenge's 45m", got)
	}
}

func TestReadyingAloneDoesNotStartAMatch(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	host := seedUser(t, l.db)
	opened := l.create(t, host, "ALONE234")

	_, _, err := l.db.ReadyUp(t.Context(), opened.ID, host)
	if !errors.Is(err, store.ErrLobbyIncomplete) {
		t.Errorf("readying alone = %v, want ErrLobbyIncomplete", err)
	}
}

func TestReadyingTwiceStartsTheMatchOnce(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, guest := l.filled(t, "TWICE234")

	if _, _, err := l.db.ReadyUp(t.Context(), match, host); err != nil {
		t.Fatalf("host readies: %v", err)
	}
	running, started, err := l.db.ReadyUp(t.Context(), match, guest)
	if err != nil || !started {
		t.Fatalf("guest readies: started=%v err=%v", started, err)
	}

	// Readying again is what a client that lost the response does. It must
	// not restart the clock, which would hand somebody a fresh 45 minutes.
	for name, player := range map[string]id.ID{"host": host, "guest": guest} {
		t.Run(name, func(t *testing.T) {
			_, again, err := l.db.ReadyUp(t.Context(), match, player)
			if !errors.Is(err, store.ErrMatchStarted) {
				t.Errorf("readying a started match = %v, want ErrMatchStarted", err)
			}
			if again {
				t.Error("readying again reported starting the match")
			}
		})
	}

	after, err := l.db.MatchForPlayer(t.Context(), match, host)
	if err != nil {
		t.Fatal(err)
	}
	if !after.StartedAt.Equal(*running.StartedAt) {
		t.Errorf("the clock moved from %v to %v", running.StartedAt, after.StartedAt)
	}
}

func TestBothSubmissionsMoveTheMatchToJudging(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, guest := l.started(t, "SUBMT234")

	waiting, judging, err := l.db.Submit(t.Context(), match, host)
	if err != nil {
		t.Fatalf("host submits: %v", err)
	}
	if judging {
		t.Error("one submission moved the match to judging, want it to wait")
	}
	if waiting.State != store.MatchActive {
		t.Errorf("state after one submission = %q, want active", waiting.State)
	}
	if waiting.Players[0].SubmittedAt == nil {
		t.Error("the player who submitted is not marked submitted")
	}

	done, judging, err := l.db.Submit(t.Context(), match, guest)
	if err != nil {
		t.Fatalf("guest submits: %v", err)
	}
	if !judging {
		t.Error("the second submission did not report moving the match")
	}
	if done.State != store.MatchJudging {
		t.Errorf("state = %q, want judging", done.State)
	}
}

// The issue's own done-when: both players submitting and the deadline firing
// are simultaneous by design, and exactly one of them may move the match.
func TestSubmittingAndTheDeadlineRaceToOneTransition(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, guest := l.started(t, "RACET234")
	l.expireNow(t, match)

	ctx := context.WithoutCancel(t.Context())
	type outcome struct {
		moved bool
		err   error
	}
	results := make(chan outcome, 3)
	start := make(chan struct{})

	for _, player := range []id.ID{host, guest} {
		go func() {
			<-start
			_, moved, err := l.db.Submit(ctx, match, player)
			results <- outcome{moved, err}
		}()
	}
	go func() {
		<-start
		moved, err := l.db.Expire(ctx, match)
		results <- outcome{moved, err}
	}()
	close(start)

	var transitions int
	for range 3 {
		got := <-results
		switch {
		case got.err == nil:
			if got.moved {
				transitions++
			}
		// A submission that arrives after the deadline has already moved the
		// match is late, not broken.
		case errors.Is(got.err, store.ErrMatchNotActive):
		default:
			t.Errorf("racing transition: %v", got.err)
		}
	}
	if transitions != 1 {
		t.Errorf("%d callers moved the match, want exactly 1", transitions)
	}

	final, err := l.db.MatchForPlayer(t.Context(), match, host)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != store.MatchJudging {
		t.Errorf("state = %q, want judging", final.State)
	}
}

func TestExpiringWaitsForTheDeadline(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, _ := l.started(t, "EXPIR234")

	moved, err := l.db.Expire(t.Context(), match)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if moved {
		t.Error("a match was expired before its deadline")
	}

	l.expireNow(t, match)
	if moved, err = l.db.Expire(t.Context(), match); err != nil || !moved {
		t.Fatalf("expire after the deadline: moved=%v err=%v", moved, err)
	}

	// Expiring an already expired match is what a second tick of the
	// finalizer does.
	if moved, err = l.db.Expire(t.Context(), match); err != nil || moved {
		t.Errorf("expiring again: moved=%v err=%v, want false and no error", moved, err)
	}

	final, err := l.db.MatchForPlayer(t.Context(), match, host)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != store.MatchJudging {
		t.Errorf("state = %q, want judging", final.State)
	}
}

func TestTransitionsRefuseTheWrongStateAndStrangers(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, _ := l.filled(t, "WRONG234")
	stranger := seedUser(t, l.db)

	if _, _, err := l.db.Submit(t.Context(), match, host); !errors.Is(err, store.ErrMatchNotActive) {
		t.Errorf("submitting before the match started = %v, want ErrMatchNotActive", err)
	}
	if _, _, err := l.db.ReadyUp(t.Context(), match, stranger); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a stranger readying = %v, want ErrNotFound", err)
	}
	if _, _, err := l.db.Submit(t.Context(), match, stranger); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a stranger submitting = %v, want ErrNotFound", err)
	}
	if _, _, err := l.db.ReadyUp(t.Context(), id.New(), host); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("readying a match that does not exist = %v, want ErrNotFound", err)
	}
}

func TestExpireDueMovesOnlyMatchesPastTheirDeadline(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	due, _, _ := l.started(t, "DUEONE23")
	running, runningHost, _ := l.started(t, "RUNNIN23")
	l.expireNow(t, due)

	expired, err := l.db.ExpireDue(t.Context(), 10)
	if err != nil {
		t.Fatalf("expire due: %v", err)
	}
	if len(expired) != 1 || expired[0] != due {
		t.Fatalf("expired %v, want just the match past its deadline (%v)", expired, due)
	}

	// The match still on the clock is untouched, and a second tick has
	// nothing left to do.
	untouched, err := l.db.MatchForPlayer(t.Context(), running, runningHost)
	if err != nil {
		t.Fatalf("read the match still on the clock: %v", err)
	}
	if untouched.State != store.MatchActive {
		t.Errorf("a match still on the clock is %q, want active", untouched.State)
	}
	again, err := l.db.ExpireDue(t.Context(), 10)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("a second tick expired %v, want nothing", again)
	}
}

// A player submitting at the deadline holds their own match row for the
// moment that takes. A tick must step over that match rather than queue
// behind it, or one slow submission delays every other match that is due.
func TestExpireDueSkipsAMatchSomebodyIsHolding(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	held, _, _ := l.started(t, "HELDON23")
	free, _, _ := l.started(t, "FREEONE3")
	l.expireNow(t, held)
	l.expireNow(t, free)

	ctx := t.Context()
	holder, err := l.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := holder.Exec(ctx, `select 1 from matches where id = $1 for update`, held); err != nil {
		t.Fatalf("hold the match: %v", err)
	}

	// This would block forever instead of skipping, if the query waited.
	expired, err := l.db.ExpireDue(ctx, 10)
	if err != nil {
		t.Fatalf("expire due while a match is held: %v", err)
	}
	if len(expired) != 1 || expired[0] != free {
		t.Fatalf("expired %v, want only the match nobody was holding (%v)", expired, free)
	}

	// Once the holder is done, the skipped match is picked up by the next
	// tick, which is why skipping loses nothing.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := l.db.ExpireDue(ctx, 10)
	if err != nil {
		t.Fatalf("next tick: %v", err)
	}
	if len(next) != 1 || next[0] != held {
		t.Errorf("next tick expired %v, want the previously held match (%v)", next, held)
	}
}

func TestExpireDueStopsAtTheLimit(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	for _, code := range []string{"BATCH234", "BATCH235", "BATCH236"} {
		match, _, _ := l.started(t, code)
		l.expireNow(t, match)
	}

	first, err := l.db.ExpireDue(t.Context(), 2)
	if err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(first) != 2 {
		t.Errorf("first tick expired %d matches, want the limit of 2", len(first))
	}

	second, err := l.db.ExpireDue(t.Context(), 2)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(second) != 1 {
		t.Errorf("second tick expired %d matches, want the remaining 1", len(second))
	}
}

// The same race as the single-match transition, through the batch path this
// time: a tick and a final submission land together, and only one of them may
// move the match.
func TestATickAndASubmissionRaceToOneTransition(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, guest := l.started(t, "TICKRA23")
	if _, _, err := l.db.Submit(t.Context(), match, host); err != nil {
		t.Fatalf("host submits: %v", err)
	}
	l.expireNow(t, match)

	ctx := context.WithoutCancel(t.Context())
	moved := make(chan int, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})

	go func() {
		<-start
		_, judging, err := l.db.Submit(ctx, match, guest)
		if err != nil && !errors.Is(err, store.ErrMatchNotActive) {
			errs <- err
		}
		if judging {
			moved <- 1
		} else {
			moved <- 0
		}
	}()
	go func() {
		<-start
		expired, err := l.db.ExpireDue(ctx, 10)
		if err != nil {
			errs <- err
		}
		moved <- len(expired)
	}()
	close(start)

	total := <-moved + <-moved
	close(errs)
	for err := range errs {
		t.Errorf("racing a tick against a submission: %v", err)
	}
	if total != 1 {
		t.Errorf("%d callers moved the match, want exactly 1", total)
	}

	final, err := l.db.MatchForPlayer(t.Context(), match, host)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != store.MatchJudging {
		t.Errorf("state = %q, want judging", final.State)
	}
}

// expireNow backdates the clock so the deadline has passed. The start moves
// with it, because the schema refuses a deadline that precedes the start.
func (l lobby) expireNow(t *testing.T, match id.ID) {
	t.Helper()

	const backdate = `update matches
		set started_at = now() - interval '1 hour', deadline_at = now() - interval '1 second'
		where id = $1`
	if _, err := l.db.Pool().Exec(t.Context(), backdate, match); err != nil {
		t.Fatalf("backdate the clock: %v", err)
	}
}
