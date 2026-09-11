package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// matchView is a match as a client sees it.
type matchView struct {
	Match struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Code    string `json:"code"`
		Players []struct {
			Username string `json:"username"`
			Slot     int    `json:"slot"`
		} `json:"players"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"match"`
}

// openLobby signs a player in, opens a lobby, and returns both.
func openLobby(t *testing.T, h *harness, name string) (*http.Cookie, matchView) {
	t.Helper()

	session := h.signIn(name)
	res := h.post("/matches", `{}`, session)
	if res.status != http.StatusCreated {
		t.Fatalf("create lobby: status %d, body %s", res.status, res.body)
	}

	var lobby matchView
	res.decode(t, &lobby)
	return session, lobby
}

func TestMatchRoutesNeedASession(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	for name, call := range map[string]func() response{
		"create":  func() response { return h.post("/matches", `{}`) },
		"join":    func() response { return h.post("/matches/join", `{"code":"ABCD2345"}`) },
		"current": func() response { return h.get("/matches/current") },
		"read":    func() response { return h.get("/matches/01912d3f-0000-7000-8000-000000000000") },
		"leave":   func() response { return h.post("/matches/01912d3f-0000-7000-8000-000000000000/leave", ``) },
	} {
		t.Run(name, func(t *testing.T) {
			res := call()
			if res.status != http.StatusUnauthorized {
				t.Errorf("%s without a session = %d, want 401", name, res.status)
			}
			if code := res.failure(t).Code; code != "unauthenticated" {
				t.Errorf("error code = %q, want unauthenticated", code)
			}
		})
	}
}

func TestCreateLobbyReturnsAShareableCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	_, lobby := openLobby(t, h, "host")

	if lobby.Match.State != "lobby" {
		t.Errorf("state = %q, want lobby", lobby.Match.State)
	}
	if len(lobby.Match.Code) != 8 {
		t.Errorf("code %q is %d characters, want 8", lobby.Match.Code, len(lobby.Match.Code))
	}
	if len(lobby.Match.Players) != 1 || lobby.Match.Players[0].Username != "host" {
		t.Errorf("players = %v, want the host alone", lobby.Match.Players)
	}
	if lobby.Match.Players[0].Slot != 1 {
		t.Errorf("host is in slot %d, want 1", lobby.Match.Players[0].Slot)
	}
}

// Waiting in a lobby must not be a way to read the challenge early.
func TestALobbyDoesNotNameItsChallenge(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	session := h.signIn("curious")
	res := h.post("/matches", `{}`, session)

	for _, secret := range []string{"todo-api", "challenge", "requirement"} {
		if strings.Contains(strings.ToLower(string(res.body)), secret) {
			t.Errorf("lobby response mentions %q: %s", secret, res.body)
		}
	}
}

func TestSecondPlayerJoinsByCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, lobby := openLobby(t, h, "host")

	guest := h.signIn("guest")
	res := h.post("/matches/join", fmt.Sprintf(`{"code":%q}`, lobby.Match.Code), guest)
	if res.status != http.StatusOK {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	var joined matchView
	res.decode(t, &joined)
	if joined.Match.ID != lobby.Match.ID {
		t.Errorf("joined match %s, want %s", joined.Match.ID, lobby.Match.ID)
	}
	if len(joined.Match.Players) != 2 {
		t.Fatalf("joined lobby holds %d players, want 2", len(joined.Match.Players))
	}
	if joined.Match.Players[1].Username != "guest" || joined.Match.Players[1].Slot != 2 {
		t.Errorf("second seat is %v, want guest in slot 2", joined.Match.Players[1])
	}

	// The host sees the guest arrive by asking again, which is what a lobby
	// screen does until the WebSocket exists.
	seen := h.get("/matches/"+lobby.Match.ID, host)
	if seen.status != http.StatusOK {
		t.Fatalf("host reads the lobby: status %d, body %s", seen.status, seen.body)
	}
	var view matchView
	seen.decode(t, &view)
	if len(view.Match.Players) != 2 {
		t.Errorf("host sees %d players, want 2", len(view.Match.Players))
	}
}

func TestJoinRefusesAFullLobbyAndABadCode(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	_, lobby := openLobby(t, h, "host")

	guest := h.signIn("guest")
	if res := h.post("/matches/join", fmt.Sprintf(`{"code":%q}`, lobby.Match.Code), guest); res.status != http.StatusOK {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	third := h.signIn("third")
	full := h.post("/matches/join", fmt.Sprintf(`{"code":%q}`, lobby.Match.Code), third)
	if full.status != http.StatusConflict {
		t.Errorf("third player = %d, want 409", full.status)
	}
	if code := full.failure(t).Code; code != "lobby_full" {
		t.Errorf("error code = %q, want lobby_full", code)
	}

	unknown := h.post("/matches/join", `{"code":"ZZZZ2345"}`, third)
	if unknown.status != http.StatusNotFound {
		t.Errorf("unknown code = %d, want 404", unknown.status)
	}

	malformed := h.post("/matches/join", `{"code":"nope"}`, third)
	if malformed.status != http.StatusBadRequest {
		t.Errorf("malformed code = %d, want 400", malformed.status)
	}
	if code := malformed.failure(t).Code; code != "invalid_code" {
		t.Errorf("error code = %q, want invalid_code", code)
	}
}

func TestAPlayerCanOnlyHoldOneMatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, _ := openLobby(t, h, "host")

	res := h.post("/matches", `{}`, host)
	if res.status != http.StatusConflict {
		t.Errorf("second lobby = %d, want 409", res.status)
	}
	if code := res.failure(t).Code; code != "in_match" {
		t.Errorf("error code = %q, want in_match", code)
	}
}

// A session says who somebody is, never what they may open.
func TestAMatchIsInvisibleToStrangers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	_, lobby := openLobby(t, h, "host")
	stranger := h.signIn("stranger")

	res := h.get("/matches/"+lobby.Match.ID, stranger)
	if res.status != http.StatusNotFound {
		t.Errorf("stranger reading a match = %d, want 404", res.status)
	}
	if code := res.failure(t).Code; code != "not_found" {
		t.Errorf("error code = %q, want not_found", code)
	}

	// A match that does not exist answers identically, so the difference is
	// not something to probe for.
	missing := h.get("/matches/01912d3f-0000-7000-8000-000000000000", stranger)
	if missing.status != res.status {
		t.Errorf("a match that does not exist = %d, a match somebody is not in = %d, want the same",
			missing.status, res.status)
	}

	// And a leave is refused the same way.
	if leave := h.post("/matches/"+lobby.Match.ID+"/leave", ``, stranger); leave.status != http.StatusNotFound {
		t.Errorf("stranger leaving somebody else's match = %d, want 404", leave.status)
	}
}

func TestCurrentMatchSurvivesAReload(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	fresh := h.signIn("nobody")
	if res := h.get("/matches/current", fresh); res.status != http.StatusNotFound {
		t.Errorf("current with no match = %d, want 404", res.status)
	}

	host, lobby := openLobby(t, h, "host")
	res := h.get("/matches/current", host)
	if res.status != http.StatusOK {
		t.Fatalf("current: status %d, body %s", res.status, res.body)
	}

	var current matchView
	res.decode(t, &current)
	if current.Match.ID != lobby.Match.ID {
		t.Errorf("current match = %s, want %s", current.Match.ID, lobby.Match.ID)
	}
	// The code comes back with it, so a reloaded lobby screen can still show
	// the player what to share.
	if current.Match.Code != lobby.Match.Code {
		t.Errorf("current code = %q, want %q", current.Match.Code, lobby.Match.Code)
	}
}

func TestLeavingCancelsTheLobbyForBothPlayers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, lobby := openLobby(t, h, "host")

	guest := h.signIn("guest")
	if res := h.post("/matches/join", fmt.Sprintf(`{"code":%q}`, lobby.Match.Code), guest); res.status != http.StatusOK {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	if res := h.post("/matches/"+lobby.Match.ID+"/leave", ``, guest); res.status != http.StatusNoContent {
		t.Fatalf("leave: status %d, body %s", res.status, res.body)
	}

	// Leaving twice is what a retried request looks like.
	if res := h.post("/matches/"+lobby.Match.ID+"/leave", ``, guest); res.status != http.StatusNoContent {
		t.Errorf("leaving again = %d, want 204", res.status)
	}

	for name, session := range map[string]*http.Cookie{"host": host, "guest": guest} {
		t.Run(name, func(t *testing.T) {
			if res := h.get("/matches/current", session); res.status != http.StatusNotFound {
				t.Errorf("%s still holds a match: %d, want 404", name, res.status)
			}
			if res := h.post("/matches", `{}`, session); res.status != http.StatusCreated {
				t.Errorf("%s opens a new lobby = %d, want 201", name, res.status)
			}
		})
	}

	// The cancelled lobby is still readable by its players, and no longer
	// carries a code, because the code is free for somebody else now.
	res := h.get("/matches/"+lobby.Match.ID, host)
	if res.status != http.StatusOK {
		t.Fatalf("host reads the cancelled lobby: status %d, body %s", res.status, res.body)
	}
	var cancelled matchView
	res.decode(t, &cancelled)
	if cancelled.Match.State != "abandoned" {
		t.Errorf("state = %q, want abandoned", cancelled.Match.State)
	}
	if cancelled.Match.Code != "" {
		t.Errorf("cancelled lobby still advertises code %q", cancelled.Match.Code)
	}
}
