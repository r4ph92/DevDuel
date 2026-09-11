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
func loadTodoAPI(t *testing.T) *challenge.Spec {
	t.Helper()

	spec, err := challenge.Load("../../challenges/todo-api")
	if err != nil {
		t.Fatalf("load challenge: %v", err)
	}
	return spec
}

func TestRegisterChallengeIsIdempotent(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec := loadTodoAPI(t)

	for range 2 {
		if err := db.RegisterChallenge(t.Context(), spec); err != nil {
			t.Fatalf("register challenge: %v", err)
		}
	}

	var count int
	const query = `select count(*) from requirements where challenge_id = $1 and challenge_version = $2`
	if err := db.Pool().QueryRow(t.Context(), query, spec.ID, spec.Version).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(spec.Requirements) {
		t.Errorf("registered %d requirements, want %d", count, len(spec.Requirements))
	}
}

// Every API instance registers the catalog as it boots, so they collide.
func TestRegisterChallengeSurvivesConcurrentRegistration(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec := loadTodoAPI(t)

	const instances = 4
	errs := make(chan error, instances)
	start := make(chan struct{})
	for range instances {
		go func() {
			<-start
			errs <- db.RegisterChallenge(context.WithoutCancel(t.Context()), spec)
		}()
	}
	close(start)

	for range instances {
		if err := <-errs; err != nil {
			t.Errorf("concurrent registration: %v", err)
		}
	}

	var count int
	const query = `select count(*) from requirements where challenge_id = $1 and challenge_version = $2`
	if err := db.Pool().QueryRow(t.Context(), query, spec.ID, spec.Version).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(spec.Requirements) {
		t.Errorf("registered %d requirements, want %d", count, len(spec.Requirements))
	}
}

// A challenge version is immutable. Changing one without bumping the version
// has to be refused, whichever part of it changed.
func TestRegisterChallengeRefusesAChangedVersion(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec := loadTodoAPI(t)

	if err := db.RegisterChallenge(t.Context(), spec); err != nil {
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

			if err := db.RegisterChallenge(t.Context(), &altered); !errors.Is(err, store.ErrChallengeConflict) {
				t.Errorf("registering a changed %s = %v, want ErrChallengeConflict", name, err)
			}
		})
	}
}

// A registration that fails partway leaves nothing behind, or the next boot
// would find a challenge with half a checklist and register nothing.
func TestRegisterChallengeRollsBackAnIncompleteRegistration(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	spec := loadTodoAPI(t)

	// A weight the schema refuses, reached after the first requirement has
	// already been inserted.
	spec.Requirements[1].Weight = 0

	if err := db.RegisterChallenge(t.Context(), spec); err == nil {
		t.Fatal("registered a requirement with no weight, want a refusal")
	}

	var challenges int
	if err := db.Pool().QueryRow(t.Context(), `select count(*) from challenges`).Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if challenges != 0 {
		t.Errorf("%d challenges survived a failed registration, want 0", challenges)
	}
}
