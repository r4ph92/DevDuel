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
	Code      string       `json:"code,omitempty"`
	Players   []playerBody `json:"players"`
	CreatedAt time.Time    `json:"created_at"`
}

// playerBody is an opponent as the other player sees them: a name and a seat,
// and nothing that belongs to the account behind it.
type playerBody struct {
	Username string `json:"username"`
	Slot     int    `json:"slot"`
}

func newMatchBody(m store.Match) matchBody {
	players := make([]playerBody, len(m.Players))
	for i, p := range m.Players {
		players[i] = playerBody{Username: p.Username, Slot: p.Slot}
	}

	body := matchBody{
		ID:        m.ID.String(),
		State:     string(m.State),
		Players:   players,
		CreatedAt: m.CreatedAt,
	}
	if m.State == store.MatchLobby {
		body.Code = m.Code
	}
	return body
}

type matchResponse struct {
	Match matchBody `json:"match"`
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
