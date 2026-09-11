package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// twoPlayerLobby is a lobby with both seats taken, which is where the
// lifecycle starts.
func twoPlayerLobby(t *testing.T, h *harness) (host, guest *http.Cookie, match string) {
	t.Helper()

	host, lobby := openLobby(t, h, "host")
	guest = h.signIn("guest")

	res := h.post("/matches/join", fmt.Sprintf(`{"code":%q}`, lobby.Match.Code), guest)
	if res.status != http.StatusOK {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}
	return host, guest, lobby.Match.ID
}

func TestTheSecondReadyStartsTheClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, guest, match := twoPlayerLobby(t, h)

	res := h.post("/matches/"+match+"/ready", ``, host)
	if res.status != http.StatusOK {
		t.Fatalf("host readies: status %d, body %s", res.status, res.body)
	}

	var waiting matchView
	res.decode(t, &waiting)
	if waiting.Match.State != "lobby" {
		t.Errorf("state after one ready = %q, want lobby", waiting.Match.State)
	}
	if !waiting.Match.Players[0].Ready || waiting.Match.Players[1].Ready {
		t.Errorf("ready flags = %v, want only the host ready", waiting.Match.Players)
	}
	if waiting.Match.StartedAt != nil || waiting.Match.DeadlineAt != nil {
		t.Error("the clock is running before both players are ready")
	}

	res = h.post("/matches/"+match+"/ready", ``, guest)
	if res.status != http.StatusOK {
		t.Fatalf("guest readies: status %d, body %s", res.status, res.body)
	}

	var running matchView
	res.decode(t, &running)
	if running.Match.State != "active" {
		t.Errorf("state = %q, want active", running.Match.State)
	}
	if running.Match.StartedAt == nil || running.Match.DeadlineAt == nil {
		t.Fatal("the match started without a clock")
	}
	if got := running.Match.DeadlineAt.Sub(*running.Match.StartedAt); got != 45*time.Minute {
		t.Errorf("match runs for %v, want the challenge's 45m", got)
	}

	// The client is told what to measure the deadline against, so it never
	// has to trust its own clock.
	if running.Match.ServerNow.IsZero() {
		t.Error("no server_now to render the timer against")
	}
	if remaining := running.Match.DeadlineAt.Sub(running.Match.ServerNow); remaining <= 0 {
		t.Errorf("time remaining is %v, want it still running", remaining)
	}

	// A started match no longer advertises a join code.
	if running.Match.Code != "" {
		t.Errorf("a started match still advertises code %q", running.Match.Code)
	}
}

func TestReadyingAloneIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, lobby := openLobby(t, h, "host")

	res := h.post("/matches/"+lobby.Match.ID+"/ready", ``, host)
	if res.status != http.StatusConflict {
		t.Errorf("readying alone = %d, want 409", res.status)
	}
	if code := res.failure(t).Code; code != "lobby_incomplete" {
		t.Errorf("error code = %q, want lobby_incomplete", code)
	}
}

func TestBothSubmissionsEndTheMatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, guest, match := twoPlayerLobby(t, h)

	for _, session := range []*http.Cookie{host, guest} {
		if res := h.post("/matches/"+match+"/ready", ``, session); res.status != http.StatusOK {
			t.Fatalf("ready: status %d, body %s", res.status, res.body)
		}
	}

	res := h.post("/matches/"+match+"/submit", ``, host)
	if res.status != http.StatusOK {
		t.Fatalf("host submits: status %d, body %s", res.status, res.body)
	}
	var waiting matchView
	res.decode(t, &waiting)
	if waiting.Match.State != "active" {
		t.Errorf("state after one submission = %q, want active", waiting.Match.State)
	}
	if !waiting.Match.Players[0].Submitted {
		t.Error("the player who submitted is not marked submitted")
	}

	res = h.post("/matches/"+match+"/submit", ``, guest)
	if res.status != http.StatusOK {
		t.Fatalf("guest submits: status %d, body %s", res.status, res.body)
	}
	var done matchView
	res.decode(t, &done)
	if done.Match.State != "judging" {
		t.Errorf("state = %q, want judging", done.Match.State)
	}

	// Submitting again is what a retried request looks like.
	if again := h.post("/matches/"+match+"/submit", ``, guest); again.status != http.StatusConflict {
		t.Errorf("submitting after the match ended = %d, want 409", again.status)
	}
}

func TestSubmittingBeforeTheMatchStarts(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, _, match := twoPlayerLobby(t, h)

	res := h.post("/matches/"+match+"/submit", ``, host)
	if res.status != http.StatusConflict {
		t.Errorf("submitting in a lobby = %d, want 409", res.status)
	}
	if code := res.failure(t).Code; code != "not_active" {
		t.Errorf("error code = %q, want not_active", code)
	}
}

// The same rule as every other match route: a session says who somebody is,
// never what they may touch.
func TestStrangersCannotReadyOrSubmit(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	_, _, match := twoPlayerLobby(t, h)
	stranger := h.signIn("stranger")

	for _, action := range []string{"ready", "submit"} {
		t.Run(action, func(t *testing.T) {
			res := h.post("/matches/"+match+"/"+action, ``, stranger)
			if res.status != http.StatusNotFound {
				t.Errorf("stranger %ss somebody else's match = %d, want 404", action, res.status)
			}
		})
	}
}
