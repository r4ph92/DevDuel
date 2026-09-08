package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// recorder remembers the status so the log line can report it. It exists
// because http.ResponseWriter will not say what was written to it.
type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// logRequests writes one line per request: method, path, status, duration.
//
// What it does not write is the body, any header, or any query string. A
// request to this API carries a password in its body and a session token in
// its cookies, and the surest way to keep those out of the logs is for the
// logging never to have been able to reach them. Paths are fixed patterns
// here, so the path is safe to record.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(started).Round(time.Millisecond),
		)
	})
}

// recoverPanics turns a panic in a handler into a 500 and a log line, so that
// one broken request does not take the process with it.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			// Not http.ErrAbortHandler: that one is the standard library's
			// way of saying the response is already forfeit, and swallowing
			// it would log a panic that is not one.
			if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(p)
			}

			log.Error("panic in handler", "method", r.Method, "path", r.URL.Path, "panic", p)
			writeError(w, log, http.StatusInternalServerError, "internal", "Something went wrong.")
		}()

		next.ServeHTTP(w, r)
	})
}
