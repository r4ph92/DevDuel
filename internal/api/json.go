package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// maxRequestBytes is what a request body may be before it is refused. Every
// body this package reads is a handful of short strings, and the ceiling is
// what stops an open socket from being a way to spend the server's memory.
const maxRequestBytes = 64 << 10

// failure is the one shape every error response takes. Code is stable and
// meant to be branched on; Message is for a person and may be reworded at any
// time.
type failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorBody wraps a failure so that a response is never a bare object whose
// meaning depends on which field happens to be present.
type errorBody struct {
	Error failure `json:"error"`
}

// writeJSON sends v with the given status.
func writeJSON(w http.ResponseWriter, log *slog.Logger, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// The status line is already decided by the time this could happen,
		// so there is nothing useful left to tell the client.
		log.Error("cannot encode response", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		log.Debug("cannot write response", "error", err)
	}
}

// writeError sends one failure.
func writeError(w http.ResponseWriter, log *slog.Logger, status int, code, message string) {
	writeJSON(w, log, status, errorBody{failure{Code: code, Message: message}})
}

// errBadRequest is what decodeJSON reports for anything wrong with the body,
// deliberately without detail: the shapes this API accepts are documented,
// and echoing a parser's complaint back is how a body ends up quoted in a
// log line somewhere.
var errBadRequest = errors.New("api: malformed request body")

// decodeJSON reads exactly one JSON object into v.
//
// Unknown fields are refused rather than ignored, so a client that misspells
// a field is told, instead of quietly registering with no password. Trailing
// content is refused for the same reason.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", errBadRequest, err)
	}
	if dec.More() {
		return errBadRequest
	}
	return nil
}
