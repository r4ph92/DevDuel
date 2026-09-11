package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
)

// A player's workspace is rows in this database rather than a directory
// somewhere, which is what lets a judge run be a container that exists for a
// few seconds and lets a reconnecting player find their code where they left
// it. These are the reads and writes over those rows.
//
// Every write is guarded on the match being active, in SQL. The window is not
// a rule the HTTP layer remembers to check: once the clock stops, the tree
// the judge will read is the tree that existed at the buzzer.

// Limits on one player's workspace. The schema caps a single file; these cap
// the tree, because nothing else does.
const (
	// MaxWorkspaceFiles is how many files one player may keep.
	MaxWorkspaceFiles = 400
	// MaxWorkspaceBytes is the total across them.
	MaxWorkspaceBytes = 8 << 20
)

// WorkspaceFile is one file in a player's tree.
type WorkspaceFile struct {
	Path      string
	Content   []byte
	UpdatedAt time.Time
}

// WorkspaceEntry is one file without its content, which is what a file tree
// needs: listing a workspace must not drag every byte of it out of the
// database to render a sidebar.
type WorkspaceEntry struct {
	Path      string
	Size      int
	UpdatedAt time.Time
}

// ListWorkspace returns the player's tree in path order, without contents.
func (q *Queries) ListWorkspace(ctx context.Context, match, user id.ID) ([]WorkspaceEntry, error) {
	const query = `select path, octet_length(content), updated_at from workspace_files
		where match_id = $1 and user_id = $2 order by path`

	rows, err := q.db.Query(ctx, query, match, user)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []WorkspaceEntry
	for rows.Next() {
		var e WorkspaceEntry
		if err := rows.Scan(&e.Path, &e.Size, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: read workspace entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read workspace: %w", err)
	}
	return out, nil
}

// ReadWorkspaceFile returns one file, or [ErrNotFound].
func (q *Queries) ReadWorkspaceFile(ctx context.Context, match, user id.ID, path string) (WorkspaceFile, error) {
	if err := validPath(path); err != nil {
		return WorkspaceFile{}, err
	}

	const query = `select path, content, updated_at from workspace_files
		where match_id = $1 and user_id = $2 and path = $3`

	var out WorkspaceFile
	err := q.db.QueryRow(ctx, query, match, user, path).Scan(&out.Path, &out.Content, &out.UpdatedAt)
	if err != nil {
		return WorkspaceFile{}, translate(err)
	}
	return out, nil
}

// WriteWorkspaceFile creates or replaces one file.
//
// It is refused unless the match is active, checked in the same statement
// that writes, so no caller can be the one that forgets. The ceilings are
// checked against the tree as it stands inside the transaction, so two
// concurrent writes cannot both squeeze past the last byte.
func (q *Queries) WriteWorkspaceFile(ctx context.Context, match, user id.ID, path string, content []byte) error {
	if err := validPath(path); err != nil {
		return err
	}
	if len(content) > challenge.MaxFileBytes {
		return ErrFileTooLarge
	}

	return q.InTx(ctx, func(q *Queries) error {
		if err := q.requireActive(ctx, match, user); err != nil {
			return err
		}

		// What the tree would weigh afterwards, counting this file's current
		// size out and its new size in.
		const room = `select
			count(*) filter (where path <> $3),
			coalesce(sum(octet_length(content)) filter (where path <> $3), 0)
			from workspace_files where match_id = $1 and user_id = $2`

		var files, bytes int
		if err := q.db.QueryRow(ctx, room, match, user, path).Scan(&files, &bytes); err != nil {
			return translate(err)
		}
		if files+1 > MaxWorkspaceFiles || bytes+len(content) > MaxWorkspaceBytes {
			return ErrWorkspaceFull
		}

		const write = `insert into workspace_files (match_id, user_id, path, content)
			values ($1, $2, $3, $4)
			on conflict (match_id, user_id, path)
			do update set content = excluded.content, updated_at = now()`

		if _, err := q.db.Exec(ctx, write, match, user, path, content); err != nil {
			return translate(err)
		}
		return nil
	})
}

// DeleteWorkspaceFile removes one file. Deleting a file that is not there
// succeeds, because the outcome the caller asked for is already true.
func (q *Queries) DeleteWorkspaceFile(ctx context.Context, match, user id.ID, path string) error {
	if err := validPath(path); err != nil {
		return err
	}

	return q.InTx(ctx, func(q *Queries) error {
		if err := q.requireActive(ctx, match, user); err != nil {
			return err
		}

		const remove = `delete from workspace_files
			where match_id = $1 and user_id = $2 and path = $3`
		if _, err := q.db.Exec(ctx, remove, match, user, path); err != nil {
			return translate(err)
		}
		return nil
	})
}

// requireActive reports whether this player may write to this match right
// now: [ErrNotFound] when they are not in it, and [ErrMatchNotActive] when
// the clock is not running.
//
// The two are separate answers on purpose. A player who is not in the match
// must not learn anything about its state, and a player who is in it should
// be told plainly that their time is up.
func (q *Queries) requireActive(ctx context.Context, match, user id.ID) error {
	const query = `select m.state from matches m
		join match_players p on p.match_id = m.id and p.user_id = $2
		where m.id = $1`

	var state MatchState
	if err := q.db.QueryRow(ctx, query, match, user).Scan(&state); err != nil {
		return translate(err)
	}
	if state != MatchActive {
		return ErrMatchNotActive
	}
	return nil
}

// validPath rejects what the schema's path constraint would reject, so that
// a bad path is an error about a path rather than a constraint violation.
func validPath(path string) error {
	switch {
	case path == "", len(path) > 512:
		return ErrInvalidPath
	case strings.HasPrefix(path, "/"), strings.HasSuffix(path, "/"), strings.Contains(path, "//"):
		return ErrInvalidPath
	case strings.ContainsRune(path, 0):
		return ErrInvalidPath
	}
	for part := range strings.SplitSeq(path, "/") {
		if part == "." || part == ".." {
			return ErrInvalidPath
		}
	}
	return nil
}
