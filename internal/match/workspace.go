package match

import (
	"context"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
)

// A player's workspace is addressed by the match and the player together, so
// a caller cannot name a file that belongs to their opponent: there is no
// path that reaches it. Authorization here is the shape of the key rather
// than a check somebody has to remember, which is the same reason the store
// enforces the writable window in SQL.

// Files lists the player's tree, without contents.
//
// Unlike the rest of these, this one asks whether the caller is in the match
// first. A stranger's tree is empty, and an empty tree and "not your match"
// would otherwise look identical to a client, which would quietly tell them
// the match exists.
func (s *Service) Files(ctx context.Context, match, user id.ID) ([]store.WorkspaceEntry, error) {
	if _, err := s.db.MatchForPlayer(ctx, match, user); err != nil {
		return nil, err
	}
	return s.db.ListWorkspace(ctx, match, user)
}

// ReadFile returns one of the player's files, or [store.ErrNotFound].
func (s *Service) ReadFile(ctx context.Context, match, user id.ID, path string) (store.WorkspaceFile, error) {
	return s.db.ReadWorkspaceFile(ctx, match, user, path)
}

// WriteFile creates or replaces one of the player's files, while the clock is
// running and not otherwise.
func (s *Service) WriteFile(ctx context.Context, match, user id.ID, path string, content []byte) error {
	return s.db.WriteWorkspaceFile(ctx, match, user, path, content)
}

// DeleteFile removes one of the player's files. Deleting what is not there
// succeeds, since the outcome asked for is already true.
func (s *Service) DeleteFile(ctx context.Context, match, user id.ID, path string) error {
	return s.db.DeleteWorkspaceFile(ctx, match, user, path)
}
