// Package api is DevDuel's HTTP surface.
//
// Routing is net/http's own ServeMux with method patterns, which is enough
// for what this API is: a small number of fixed paths. A router dependency
// would buy parameter extraction that the standard library now does anyway.
//
// Sessions are carried in an HttpOnly cookie rather than in a header. The web
// client is a browser, and a token in a cookie the page cannot read survives
// a cross-site scripting bug that a token in localStorage does not.
package api

import (
	"log/slog"
	"net/http"

	"github.com/r4ph92/DevDuel/internal/auth"
)

// Config is what the API needs to exist.
type Config struct {
	// Auth registers accounts and opens sessions.
	Auth *auth.Service
	// Logger receives one line per request. Required.
	Logger *slog.Logger
	// SecureCookies marks the session cookie as https only. It defaults off
	// so that http://localhost works, and every deployment that is not a
	// developer's laptop has to turn it on.
	SecureCookies bool
}

// server holds what the handlers share.
type server struct {
	auth   *auth.Service
	log    *slog.Logger
	secure bool
}

// New returns the API's handler.
func New(cfg Config) http.Handler {
	s := &server{auth: cfg.Auth, log: cfg.Logger, secure: cfg.SecureCookies}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /auth/register", s.register)
	mux.HandleFunc("POST /auth/login", s.login)
	mux.HandleFunc("POST /auth/logout", s.logout)

	// Recovery outermost, so that a panic inside the logging middleware's own
	// call to the handler is still caught, and the request is still logged.
	return recoverPanics(cfg.Logger, logRequests(cfg.Logger, mux))
}

// health answers whether the process is up. It deliberately does not ask the
// database: a liveness check that fails when a dependency is down gets the
// process restarted for somebody else's outage.
func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.log, http.StatusOK, map[string]string{"status": "ok"})
}
