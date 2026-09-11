package store

import (
	"context"
	"errors"
	"slices"

	"github.com/r4ph92/DevDuel/internal/challenge"
)

// ErrChallengeConflict means a version already registered differs from the
// spec now being registered. A challenge version is immutable, so the fix is
// a new version rather than an edit.
var ErrChallengeConflict = errors.New("store: challenge version differs from its registration")

// RegisterChallenge records a validated challenge version and its
// requirements, and is safe to run repeatedly.
//
// Registering is idempotent rather than a migration, which is what lets every
// API instance do it at boot: the first one to arrive inserts, and the rest
// check that what is already there is what they were going to write. A
// difference is [ErrChallengeConflict], because a played match has to stay
// explicable by the exact spec it ran under.
func (q *Queries) RegisterChallenge(ctx context.Context, spec *challenge.Spec) error {
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

		// Nothing inserted means this version is already registered. The
		// insert waited for a concurrent registration of the same version to
		// finish first, so its requirements are visible to the check below.
		if tag.RowsAffected() == 0 {
			return q.checkRegistration(ctx, spec)
		}

		const insertRequirement = `insert into requirements
			(challenge_id, challenge_version, key, position, title, description, weight, broken)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`

		// Position is the author's order from the spec, which is part of how
		// the checklist reads and is not recoverable from the keys.
		for position, r := range spec.Requirements {
			_, err := q.db.Exec(ctx, insertRequirement, spec.ID, spec.Version,
				r.Key, position, r.Title, r.Description, r.Weight, r.Broken)
			if err != nil {
				return translate(err)
			}
		}
		return nil
	})
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
