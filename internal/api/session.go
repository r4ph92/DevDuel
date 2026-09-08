package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/r4ph92/DevDuel/internal/auth"
	"github.com/r4ph92/DevDuel/internal/store"
)

type userContextKey struct{}

// UserFromContext returns the account authenticated for this request.
// Resource handlers must separately check whether this user may access it.
func UserFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userContextKey{}).(store.User)
	return u, ok
}

// requireUser protects a route without changing or extending its cookie.
func (s *server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, err := r.Cookie(SessionCookie)
		if err != nil {
			writeError(w, s.log, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		user, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if errors.Is(err, auth.ErrInvalidSession) {
			writeError(w, s.log, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		if err != nil {
			s.log.Error("cannot authenticate session", "error", err)
			writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	})
}

type meResponse struct {
	User userBody `json:"user"`
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	writeJSON(w, s.log, http.StatusOK, meResponse{User: newUserBody(user)})
}
