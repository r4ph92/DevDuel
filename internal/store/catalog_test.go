package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// loadTodoAPI is the real challenge in the repository, so these tests register
// what production would register rather than a hand-built spec.
func loadTodoAPI(t *testing.T) (*challenge.Spec, []challenge.File) {
	t.Helper()

	spec, err := challenge.Load("../../challenges/todo-api")
	if err != nil {
		t.Fatalf("load challenge: %v", err)
	}
	files, err := spec.Workspace()
	if err != nil {
		t.Fatalf("read starting workspace: %v", err)
	}
	return spec, files
}

func TestRegisterChallengeIsIdempotent(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, files := loadTodoAPI(t)

	for range 2 {
		if err := db.RegisterChallenge(t.Context(), spec, files); err != nil {
			t.Fatalf("register challenge: %v", err)
		}
	}

	var requirements, stored int
	const counts = `select
		(select count(*) from requirements where challenge_id = $1 and challenge_version = $2),
		(select count(*) from challenge_files where challenge_id = $1 and challenge_version = $2)`
	if err := db.Pool().QueryRow(t.Context(), counts, spec.ID, spec.Version).Scan(&requirements, &stored); err != nil {
		t.Fatal(err)
	}
	if requirements != len(spec.Requirements) {
		t.Errorf("registered %d requirements, want %d", requirements, len(spec.Requirements))
	}
	if stored != len(files) {
		t.Errorf("stored %d files, want %d", stored, len(files))
	}
}

// The starting workspace is what both players will be handed, so it has to
// arrive byte for byte.
func TestRegisterChallengeStoresTheWorkspaceVerbatim(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, files := loadTodoAPI(t)

	if err := db.RegisterChallenge(t.Context(), spec, files); err != nil {
		t.Fatalf("register challenge: %v", err)
	}

	for _, want := range files {
		var content []byte
		const query = `select content from challenge_files
			where challenge_id = $1 and challenge_version = $2 and path = $3`
		err := db.Pool().QueryRow(t.Context(), query, spec.ID, spec.Version, want.Path).Scan(&content)
		if err != nil {
			t.Fatalf("read %s: %v", want.Path, err)
		}
		if !slices.Equal(content, want.Content) {
			t.Errorf("%s was stored as %d bytes, want %d", want.Path, len(content), len(want.Content))
		}
	}
}

// A version registered before challenge_files existed has a challenge row and
// no files. Registering again has to fill them in, or every match on that
// version seeds an empty workspace and nothing complains.
func TestRegisterChallengeBackfillsAMissingWorkspace(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, files := loadTodoAPI(t)

	if err := db.RegisterChallenge(t.Context(), spec, files); err != nil {
		t.Fatalf("register challenge: %v", err)
	}

	const wipe = `delete from challenge_files where challenge_id = $1 and challenge_version = $2`
	if _, err := db.Pool().Exec(t.Context(), wipe, spec.ID, spec.Version); err != nil {
		t.Fatalf("wipe the stored workspace: %v", err)
	}

	if err := db.RegisterChallenge(t.Context(), spec, files); err != nil {
		t.Fatalf("register again: %v", err)
	}

	var stored int
	const count = `select count(*) from challenge_files where challenge_id = $1 and challenge_version = $2`
	if err := db.Pool().QueryRow(t.Context(), count, spec.ID, spec.Version).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != len(files) {
		t.Errorf("backfilled %d files, want %d", stored, len(files))
	}
}

// Every API instance registers the catalog as it boots, so they collide.
func TestRegisterChallengeSurvivesConcurrentRegistration(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, files := loadTodoAPI(t)

	const instances = 4
	errs := make(chan error, instances)
	start := make(chan struct{})
	for range instances {
		go func() {
			<-start
			errs <- db.RegisterChallenge(context.WithoutCancel(t.Context()), spec, files)
		}()
	}
	close(start)

	for range instances {
		if err := <-errs; err != nil {
			t.Errorf("concurrent registration: %v", err)
		}
	}

	var stored int
	const count = `select count(*) from challenge_files where challenge_id = $1 and challenge_version = $2`
	if err := db.Pool().QueryRow(t.Context(), count, spec.ID, spec.Version).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != len(files) {
		t.Errorf("stored %d files, want %d", stored, len(files))
	}
}

// A challenge version is immutable. Changing one without bumping the version
// has to be refused, whichever part of it changed.
func TestRegisterChallengeRefusesAChangedVersion(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, files := loadTodoAPI(t)

	if err := db.RegisterChallenge(t.Context(), spec, files); err != nil {
		t.Fatalf("register challenge: %v", err)
	}

	for name, change := range map[string]func(*challenge.Spec){
		"duration":      func(s *challenge.Spec) { s.Duration += time.Minute },
		"image":         func(s *challenge.Spec) { s.Image.Tag += "-changed" },
		"difficulty":    func(s *challenge.Spec) { s.Difficulty = challenge.DifficultyHard },
		"title":         func(s *challenge.Spec) { s.Requirements[0].Title += " changed" },
		"weight":        func(s *challenge.Spec) { s.Requirements[0].Weight++ },
		"order":         func(s *challenge.Spec) { s.Requirements[0], s.Requirements[1] = s.Requirements[1], s.Requirements[0] },
		"a missing one": func(s *challenge.Spec) { s.Requirements = s.Requirements[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			altered := *spec
			altered.Requirements = slices.Clone(spec.Requirements)
			change(&altered)

			if err := db.RegisterChallenge(t.Context(), &altered, files); !errors.Is(err, store.ErrChallengeConflict) {
				t.Errorf("registering a changed %s = %v, want ErrChallengeConflict", name, err)
			}
		})
	}
}

// The starting workspace is part of the version. Editing a file, adding one
// or dropping one is a new version, never an edit of this one.
func TestRegisterChallengeRefusesAChangedWorkspace(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, files := loadTodoAPI(t)

	if err := db.RegisterChallenge(t.Context(), spec, files); err != nil {
		t.Fatalf("register challenge: %v", err)
	}

	for name, change := range map[string]func([]challenge.File) []challenge.File{
		"an edited file": func(f []challenge.File) []challenge.File {
			f[0].Content = append(slices.Clone(f[0].Content), []byte("// edited")...)
			return f
		},
		"a renamed file": func(f []challenge.File) []challenge.File {
			f[0].Path = "renamed-" + f[0].Path
			return f
		},
		"an added file": func(f []challenge.File) []challenge.File {
			return append(f, challenge.File{Path: "extra.js", Content: []byte("extra")})
		},
		"a removed file": func(f []challenge.File) []challenge.File { return f[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			altered := change(slices.Clone(files))

			if err := db.RegisterChallenge(t.Context(), spec, altered); !errors.Is(err, store.ErrChallengeConflict) {
				t.Errorf("registering %s = %v, want ErrChallengeConflict", name, err)
			}
		})
	}
}

// A registration that fails partway leaves nothing behind, or the next boot
// would find a challenge with half a checklist and register nothing.
func TestRegisterChallengeRollsBackAnIncompleteRegistration(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, files := loadTodoAPI(t)

	// A weight the schema refuses, reached after the first requirement has
	// already been inserted.
	spec.Requirements[1].Weight = 0

	if err := db.RegisterChallenge(t.Context(), spec, files); err == nil {
		t.Fatal("registered a requirement with no weight, want a refusal")
	}

	var challenges, stored int
	const counts = `select (select count(*) from challenges), (select count(*) from challenge_files)`
	if err := db.Pool().QueryRow(t.Context(), counts).Scan(&challenges, &stored); err != nil {
		t.Fatal(err)
	}
	if challenges != 0 || stored != 0 {
		t.Errorf("%d challenges and %d files survived a failed registration, want none", challenges, stored)
	}
}

// Registering without a workspace is a bug in the caller, not a challenge
// with an empty editor.
func TestRegisterChallengeNeedsAWorkspace(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec, _ := loadTodoAPI(t)

	if err := db.RegisterChallenge(t.Context(), spec, nil); err == nil {
		t.Error("registered a challenge with no starting workspace, want a refusal")
	}
}
