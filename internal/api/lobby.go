package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/match"
	"github.com/r4ph92/DevDuel/internal/store"
)

// matchBody is how a match appears in a response.
//
// There is no challenge in it. A lobby that named its challenge would let
// both players read the problem while they wait, which is the whole reason
// the match has a start rather than beginning when the second player arrives.
type matchBody struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Code is the join code, and is dropped once the match leaves the lobby:
	// codes are recycled from that moment, so a stale one names a lobby that
	// belongs to somebody else.
	Code    string       `json:"code,omitempty"`
	Players []playerBody `json:"players"`
	// StartedAt and DeadlineAt appear once the match is running. The clock is
	// the server's: a client renders the time left as DeadlineAt minus
	// ServerNow, and never from its own clock, which may be wrong by minutes.
	StartedAt  *time.Time `json:"started_at,omitempty"`
	DeadlineAt *time.Time `json:"deadline_at,omitempty"`
	// ServerNow is what the clock above is to be read against, and is sent
	// with every match so a client can correct for its own drift.
	ServerNow time.Time `json:"server_now"`
	CreatedAt time.Time `json:"created_at"`
}

// playerBody is an opponent as the other player sees them: a name and a seat,
// and nothing that belongs to the account behind it.
type playerBody struct {
	Username string `json:"username"`
	Slot     int    `json:"slot"`
	// Ready and Submitted are what the other player is allowed to know: that
	// somebody is waiting or done, never when or what they wrote.
	Ready     bool `json:"ready"`
	Submitted bool `json:"submitted"`
}

func newMatchBody(m store.Match) matchBody {
	players := make([]playerBody, len(m.Players))
	for i, p := range m.Players {
		players[i] = playerBody{
			Username:  p.Username,
			Slot:      p.Slot,
			Ready:     p.ReadyAt != nil,
			Submitted: p.SubmittedAt != nil,
		}
	}

	body := matchBody{
		ID:         m.ID.String(),
		State:      string(m.State),
		Players:    players,
		StartedAt:  utc(m.StartedAt),
		DeadlineAt: utc(m.DeadlineAt),
		ServerNow:  time.Now().UTC(),
		CreatedAt:  m.CreatedAt.UTC(),
	}
	if m.State == store.MatchLobby {
		body.Code = m.Code
	}
	return body
}

type matchResponse struct {
	Match matchBody `json:"match"`
}

// utc renders a timestamp in UTC so that every time in one response has the
// same shape. The database hands back whatever offset the session happened to
// have, and a client comparing a deadline against server_now should not have
// to notice that the two arrived spelled differently.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	in := t.UTC()
	return &in
}

func (s *server) createLobby(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	lobby, err := s.match.Create(r.Context(), user.ID)
	if err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	writeJSON(w, s.log, http.StatusCreated, matchResponse{Match: newMatchBody(lobby)})
}

type joinRequest struct {
	Code string `json:"code"`
}

func (s *server) joinLobby(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	var in joinRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, s.log, http.StatusBadRequest, "bad_request", "The request body is not valid JSON.")
		return
	}

	lobby, err := s.match.Join(r.Context(), user.ID, in.Code)
	if err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	writeJSON(w, s.log, http.StatusOK, matchResponse{Match: newMatchBody(lobby)})
}

func (s *server) currentMatch(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	current, err := s.match.Current(r.Context(), user.ID)
	if err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	writeJSON(w, s.log, http.StatusOK, matchResponse{Match: newMatchBody(current)})
}

func (s *server) readMatch(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	// An unparseable id is answered exactly like a match somebody is not in,
	// so that the shape of an id is not something to probe for either.
	target, err := id.Parse(r.PathValue("id"))
	if err != nil {
		s.writeMatchError(w, r, store.ErrNotFound)
		return
	}

	found, err := s.match.Get(r.Context(), target, user.ID)
	if err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	writeJSON(w, s.log, http.StatusOK, matchResponse{Match: newMatchBody(found)})
}

func (s *server) leaveMatch(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	target, err := id.Parse(r.PathValue("id"))
	if err != nil {
		s.writeMatchError(w, r, store.ErrNotFound)
		return
	}

	if err := s.match.Leave(r.Context(), target, user.ID); err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) readyMatch(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, func(match, user id.ID) (store.Match, error) {
		updated, _, err := s.match.Ready(r.Context(), match, user)
		return updated, err
	})
}

func (s *server) submitMatch(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, func(match, user id.ID) (store.Match, error) {
		updated, _, err := s.match.Submit(r.Context(), match, user)
		return updated, err
	})
}

// transition is the shape both readying and submitting take: identify the
// caller, read the match out of the path, act, and answer with the match as
// it now stands. Whether this caller was the one that moved the match is not
// in the answer, because both players are told the same thing either way.
func (s *server) transition(w http.ResponseWriter, r *http.Request, act func(match, user id.ID) (store.Match, error)) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}

	target, err := id.Parse(r.PathValue("id"))
	if err != nil {
		s.writeMatchError(w, r, store.ErrNotFound)
		return
	}

	updated, err := act(target, user.ID)
	if err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	writeJSON(w, s.log, http.StatusOK, matchResponse{Match: newMatchBody(updated)})
}

// writeMatchError turns what the match service reports into a response.
//
// A match the caller is not in is a 404 rather than a 403, the same answer as
// a match that does not exist: a 403 would confirm that an id names a real
// match, which is a way of counting other people's games.
func (s *server) writeMatchError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, match.ErrInvalidCode):
		writeError(w, s.log, http.StatusBadRequest, "invalid_code",
			"A join code is 8 characters of letters and digits.")

	case errors.Is(err, store.ErrNotFound):
		writeError(w, s.log, http.StatusNotFound, "not_found",
			"That match is not available.")

	case errors.Is(err, store.ErrLobbyFull):
		writeError(w, s.log, http.StatusConflict, "lobby_full",
			"That match already has two players.")

	case errors.Is(err, store.ErrInUnfinishedMatch):
		writeError(w, s.log, http.StatusConflict, "in_match",
			"You are already in a match. Leave it before starting another.")

	case errors.Is(err, store.ErrMatchStarted):
		writeError(w, s.log, http.StatusConflict, "already_started",
			"That match has already started.")

	case errors.Is(err, store.ErrLobbyIncomplete):
		writeError(w, s.log, http.StatusConflict, "lobby_incomplete",
			"Wait for another player before starting.")

	case errors.Is(err, store.ErrMatchNotActive):
		writeError(w, s.log, http.StatusConflict, "not_active",
			"That match is not running.")

	case errors.Is(err, store.ErrInvalidPath):
		writeError(w, s.log, http.StatusBadRequest, "invalid_path",
			"A file path is relative, and cannot climb out of the workspace.")

	case errors.Is(err, store.ErrFileTooLarge):
		writeError(w, s.log, http.StatusRequestEntityTooLarge, "file_too_large",
			"That file is larger than this workspace allows.")

	case errors.Is(err, store.ErrWorkspaceFull):
		writeError(w, s.log, http.StatusConflict, "workspace_full",
			"This workspace has no room left. Delete something first.")

	case errors.Is(err, store.ErrNoStartingWorkspace):
		// The catalog is registered at boot with its files, so this is a
		// deployment that registered a challenge without a workspace.
		s.log.Error("challenge has no starting workspace", "method", r.Method, "path", r.URL.Path)
		writeError(w, s.log, http.StatusServiceUnavailable, "unavailable",
			"That challenge cannot be played right now.")

	case errors.Is(err, match.ErrNoChallenges):
		// The catalog is registered before the server listens, so this is the
		// deployment being wrong rather than the request.
		s.log.Error("no challenges are available", "method", r.Method, "path", r.URL.Path)
		writeError(w, s.log, http.StatusServiceUnavailable, "unavailable",
			"No challenges are available right now.")

	default:
		s.log.Error("match request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
