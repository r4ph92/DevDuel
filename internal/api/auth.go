package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/r4ph92/DevDuel/internal/auth"
	"github.com/r4ph92/DevDuel/internal/store"
)

// SessionCookie is the name of the cookie a session travels in.
const SessionCookie = "devduel_session"

// userBody is how an account appears in a response. It is written out field by
// field rather than by tagging [store.User], so that adding a column to that
// struct cannot quietly add it to every response that carries a user.
type userBody struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func newUserBody(u store.User) userBody {
	return userBody{
		ID:        u.ID.String(),
		Username:  u.Username,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
	}
}

type registerRequest struct {
	Email    string `json:"email"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type registerResponse struct {
	User userBody `json:"user"`
}

func (s *server) register(w http.ResponseWriter, r *http.Request) {
	var in registerRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, s.log, http.StatusBadRequest, "bad_request", "The request body is not valid JSON.")
		return
	}

	user, err := s.auth.Register(r.Context(), auth.Registration{
		Email:    in.Email,
		Username: in.Username,
		Password: in.Password,
	})
	if err != nil {
		s.writeAuthError(w, r, err)
		return
	}

	// No session. Registering and signing in are separate steps, so there is
	// one place that opens a session and one thing to reason about when
	// something is wrong with how they are opened.
	writeJSON(w, s.log, http.StatusCreated, registerResponse{User: newUserBody(user)})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	User      userBody  `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, s.log, http.StatusBadRequest, "bad_request", "The request body is not valid JSON.")
		return
	}

	session, user, err := s.auth.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		s.writeAuthError(w, r, err)
		return
	}

	s.setSession(w, session)
	writeJSON(w, s.log, http.StatusOK, loginResponse{
		User:      newUserBody(user),
		ExpiresAt: session.ExpiresAt,
	})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	// Signing out with no session is what a client with a stale tab does, and
	// the outcome it asked for is already true.
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		if err := s.auth.Logout(r.Context(), cookie.Value); err != nil {
			s.log.Error("cannot end session", "error", err)
			writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
			return
		}
	}

	s.clearSession(w)
	w.WriteHeader(http.StatusNoContent)
}

// setSession puts the token where a browser will send it back.
func (s *server) setSession(w http.ResponseWriter, session auth.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:  SessionCookie,
		Value: session.Token,
		Path:  "/",
		// Expires rather than a session cookie, so that closing the browser
		// does not sign somebody out of a match they are in the middle of.
		Expires: session.ExpiresAt,
		// The page never needs to read this, and a script that has been
		// injected into the page must not be able to either.
		HttpOnly: true,
		Secure:   s.secure,
		// Lax, so the cookie is not attached to a cross-site POST. Every
		// route that changes anything is a POST, which makes this the
		// cross-site request forgery defence for all of them.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// writeAuthError turns what the auth package reports into a response.
//
// The two that matter are the two that say nothing: a failed login is always
// the same 401 whether the address is unknown or the password is wrong, and a
// collision on registration is always the same 409 whether it was the address
// or the name. Both cases know more than they say, on purpose.
func (s *server) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, s.log, http.StatusUnauthorized, "invalid_credentials",
			"That email address and password do not match an account.")

	case errors.Is(err, auth.ErrTaken):
		writeError(w, s.log, http.StatusConflict, "taken",
			"That email address or username is not available.")

	case errors.Is(err, auth.ErrInvalidEmail):
		writeError(w, s.log, http.StatusBadRequest, "invalid_email",
			"That does not look like an email address.")

	case errors.Is(err, auth.ErrInvalidUsername):
		writeError(w, s.log, http.StatusBadRequest, "invalid_username",
			"A username is 3 to 32 characters of letters, digits, hyphens and underscores, starting with a letter or a digit.")

	case errors.Is(err, auth.ErrPasswordTooShort):
		writeError(w, s.log, http.StatusBadRequest, "password_too_short",
			"A password needs at least 8 characters.")

	case errors.Is(err, auth.ErrPasswordTooLong):
		writeError(w, s.log, http.StatusBadRequest, "password_too_long",
			"A password can be at most 256 characters.")

	default:
		// The error goes to the log and a sentence goes to the client. What
		// went wrong inside is not the caller's business, and an error string
		// is a good way to leak a query or an address into a browser.
		s.log.Error("auth failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
