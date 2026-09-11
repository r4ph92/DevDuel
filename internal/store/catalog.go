package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/r4ph92/DevDuel/internal/challenge"
)

// ErrChallengeConflict means a version already registered differs from the
// spec now being registered. A challenge version is immutable, so the fix is
// a new version rather than an edit.
var ErrChallengeConflict = errors.New("store: challenge version differs from its registration")

// RegisterChallenge records a validated challenge version, its requirements
// and its starting workspace, and is safe to run repeatedly.
//
// Registering is idempotent rather than a migration, which is what lets every
// API instance do it at boot: the first one to arrive inserts, and the rest
// check that what is already there is what they were going to write. A
// difference is [ErrChallengeConflict], because a played match has to stay
// explicable by the exact spec it ran under.
func (q *Queries) RegisterChallenge(ctx context.Context, spec *challenge.Spec, files []challenge.File) error {
	return q.InTx(ctx, func(q *Queries) error {
		// Duration is handed over as seconds and turned into an interval by
		// Postgres, so that the unit lives in one place rather than in every
		// caller that has to build one.
		const insert = `insert into challenges (id, version, category, difficulty, duration, image_tag)
			values ($1, $2, $3, $4, make_interval(secs => $5), $6)
			on conflict (id, version) do nothing`

		tag, err := q.db.Exec(ctx, insert, spec.ID, spec.Version, string(spec.Category),
			string(spec.Difficulty), spec.Duration.Seconds(), spec.Image.Tag)
		if err != nil {
			return translate(err)
		}

		if tag.RowsAffected() == 0 {
			// Nothing inserted means this version is already registered. The
			// insert waited for a concurrent registration of the same version
			// to finish, so its rows are visible to the checks below.
			if err := q.checkRegistration(ctx, spec); err != nil {
				return err
			}
		} else {
			const insertRequirement = `insert into requirements
				(challenge_id, challenge_version, key, position, title, description, weight, broken)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`

			// Position is the author's order from the spec, which is part of
			// how the checklist reads and is not recoverable from the keys.
			for position, r := range spec.Requirements {
				_, err := q.db.Exec(ctx, insertRequirement, spec.ID, spec.Version,
					r.Key, position, r.Title, r.Description, r.Weight, r.Broken)
				if err != nil {
					return translate(err)
				}
			}
		}

		// Deliberately outside that branch. A version registered before
		// challenge_files existed has a row in challenges and nothing here,
		// and a workspace that is quietly missing only shows up as two
		// players staring at an empty editor.
		return q.registerFiles(ctx, spec.Key(), files)
	})
}

// registerFiles stores the starting workspace, once, and then checks that
// what is stored is what the spec carries.
//
// Files are written only when the version has none. That is what tells a
// backfill apart from an edit: inserting whatever the caller brought would
// let a file added to an already registered version slip in, and the
// comparison afterwards would then happily agree with itself.
func (q *Queries) registerFiles(ctx context.Context, key challenge.Key, files []challenge.File) error {
	if len(files) == 0 {
		return fmt.Errorf("store: challenge %s has no starting workspace", key)
	}

	const count = `select count(*) from challenge_files
		where challenge_id = $1 and challenge_version = $2`

	var stored int
	if err := q.db.QueryRow(ctx, count, key.ID, key.Version).Scan(&stored); err != nil {
		return translate(err)
	}

	if stored == 0 {
		const insert = `insert into challenge_files (challenge_id, challenge_version, path, content)
			values ($1, $2, $3, $4) on conflict do nothing`

		// on conflict do nothing, because two instances booting together both
		// find an empty workspace and both try to fill it.
		for _, f := range files {
			if _, err := q.db.Exec(ctx, insert, key.ID, key.Version, f.Path, f.Content); err != nil {
				return translate(err)
			}
		}
	}

	return q.checkWorkspace(ctx, key, files)
}

// checkWorkspace compares the stored workspace with the one on disk.
//
// By digest rather than by content: the check has to notice an edited file,
// and carrying every byte back out of the database on every boot to find that
// out would make startup pay for a check that almost always passes.
func (q *Queries) checkWorkspace(ctx context.Context, key challenge.Key, files []challenge.File) error {
	const query = `select path, encode(sha256(content), 'hex') from challenge_files
		where challenge_id = $1 and challenge_version = $2`

	rows, err := q.db.Query(ctx, query, key.ID, key.Version)
	if err != nil {
		return translate(err)
	}
	defer rows.Close()

	// A map rather than two ordered lists: Postgres orders text by the
	// database's collation and Go orders by bytes, and those disagree the
	// moment a path stops being ASCII.
	registered := make(map[string]string, len(files))
	for rows.Next() {
		var path, digest string
		if err := rows.Scan(&path, &digest); err != nil {
			return fmt.Errorf("store: read challenge file: %w", err)
		}
		registered[path] = digest
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read challenge files: %w", err)
	}

	if len(registered) != len(files) {
		return ErrChallengeConflict
	}
	for _, f := range files {
		if registered[f.Path] != f.Digest() {
			return ErrChallengeConflict
		}
	}
	return nil
}

// checkRegistration compares an already registered version with the spec.
func (q *Queries) checkRegistration(ctx context.Context, spec *challenge.Spec) error {
	const stored = `select category, difficulty, duration = make_interval(secs => $3), image_tag
		from challenges where id = $1 and version = $2`

	var category, difficulty, imageTag string
	var sameDuration bool
	err := q.db.QueryRow(ctx, stored, spec.ID, spec.Version, spec.Duration.Seconds()).
		Scan(&category, &difficulty, &sameDuration, &imageTag)
	if err != nil {
		return translate(err)
	}
	if category != string(spec.Category) || difficulty != string(spec.Difficulty) ||
		!sameDuration || imageTag != spec.Image.Tag {
		return ErrChallengeConflict
	}

	const requirements = `select key, title, description, weight, broken from requirements
		where challenge_id = $1 and challenge_version = $2 order by position`

	rows, err := q.db.Query(ctx, requirements, spec.ID, spec.Version)
	if err != nil {
		return translate(err)
	}
	defer rows.Close()

	var registered []challenge.Requirement
	for rows.Next() {
		var r challenge.Requirement
		if err := rows.Scan(&r.Key, &r.Title, &r.Description, &r.Weight, &r.Broken); err != nil {
			return err
		}
		registered = append(registered, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Compared in order, so reordering the checklist is a conflict too.
	if !slices.Equal(registered, spec.Requirements) {
		return ErrChallengeConflict
	}
	return nil
}
