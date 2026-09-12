package api

import (
	"io"
	"net/http"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
)

// A workspace is addressed by the match and the player together, and the
// player is whoever the session says it is. There is therefore no path on
// this API that names the opponent's files: isolation is the shape of the
// route rather than a check that has to be remembered.
//
// File contents travel as raw bytes rather than inside JSON. The column is
// bytea because a file may hold a NUL, and wrapping a source file in JSON
// would mean base64 on every keystroke-driven save.

// fileEntry is one file in the tree, without its content: rendering a sidebar
// must not drag every byte of a workspace across the wire.
type fileEntry struct {
	Path      string    `json:"path"`
	Size      int       `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

type treeResponse struct {
	Files []fileEntry `json:"files"`
}

func (s *server) listFiles(w http.ResponseWriter, r *http.Request) {
	match, user, ok := s.workspaceTarget(w, r)
	if !ok {
		return
	}

	entries, err := s.match.Files(r.Context(), match, user)
	if err != nil {
		s.writeMatchError(w, r, err)
		return
	}

	files := make([]fileEntry, len(entries))
	for i, e := range entries {
		files[i] = fileEntry{Path: e.Path, Size: e.Size, UpdatedAt: e.UpdatedAt.UTC()}
	}
	writeJSON(w, s.log, http.StatusOK, treeResponse{Files: files})
}

func (s *server) readFile(w http.ResponseWriter, r *http.Request) {
	match, user, ok := s.workspaceTarget(w, r)
	if !ok {
		return
	}

	file, err := s.match.ReadFile(r.Context(), match, user, r.PathValue("path"))
	if err != nil {
		s.writeMatchError(w, r, err)
		return
	}

	// Deliberately not sniffed into a friendlier type: what a player wrote is
	// returned as bytes, and the client knows what it asked for.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(file.Content); err != nil {
		s.log.Debug("cannot write file to response", "error", err)
	}
}

func (s *server) writeFile(w http.ResponseWriter, r *http.Request) {
	match, user, ok := s.workspaceTarget(w, r)
	if !ok {
		return
	}

	// One byte over the limit, so that a file exactly at the limit is
	// accepted and anything past it is refused here with a clear answer
	// rather than by a constraint deeper down.
	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, challenge.MaxFileBytes+1))
	if err != nil {
		s.writeMatchError(w, r, store.ErrFileTooLarge)
		return
	}
	if len(content) > challenge.MaxFileBytes {
		s.writeMatchError(w, r, store.ErrFileTooLarge)
		return
	}

	if err := s.match.WriteFile(r.Context(), match, user, r.PathValue("path"), content); err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) deleteFile(w http.ResponseWriter, r *http.Request) {
	match, user, ok := s.workspaceTarget(w, r)
	if !ok {
		return
	}

	if err := s.match.DeleteFile(r.Context(), match, user, r.PathValue("path")); err != nil {
		s.writeMatchError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// workspaceTarget resolves who is asking and which match they mean, and
// answers the request itself when either is not available.
func (s *server) workspaceTarget(w http.ResponseWriter, r *http.Request) (match id.ID, user id.ID, ok bool) {
	account, found := UserFromContext(r.Context())
	if !found {
		writeError(w, s.log, http.StatusInternalServerError, "internal", "Something went wrong.")
		return id.ID{}, id.ID{}, false
	}

	// An id that cannot be parsed is answered like a match somebody is not
	// in, for the same reason: the shape of an id is not worth probing for.
	target, err := id.Parse(r.PathValue("id"))
	if err != nil {
		s.writeMatchError(w, r, store.ErrNotFound)
		return id.ID{}, id.ID{}, false
	}
	return target, account.ID, true
}
