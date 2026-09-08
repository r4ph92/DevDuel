package store_test

import (
	"errors"
	"reflect"
	"regexp"
	"testing"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// newUser is a registration with the fields a test does not care about
// already filled in.
func newUser(name string) store.NewUser {
	return store.NewUser{
		Email:        name + "@example.test",
		Username:     name,
		PasswordHash: "argon2id$placeholder",
	}
}

func TestCreateUserOpensARating(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	user, err := db.CreateUser(ctx, newUser("rated"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID.IsNil() {
		t.Error("CreateUser returned a user with no id")
	}
	if user.CreatedAt.IsZero() {
		t.Error("CreateUser returned a user with no created_at")
	}

	rating, err := db.Rating(ctx, user.ID)
	if err != nil {
		t.Fatalf("Rating: %v", err)
	}
	if rating.Value != store.DefaultRating {
		t.Errorf("rating = %d, want %d", rating.Value, store.DefaultRating)
	}
	if rating.Games != 0 {
		t.Errorf("games = %d, want 0", rating.Games)
	}
}

func TestCreateUserRefusesAnAccountThatExists(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	if _, err := db.CreateUser(ctx, newUser("taken")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	for name, tc := range map[string]struct {
		in   store.NewUser
		want error
	}{
		"same email": {
			in:   store.NewUser{Email: "taken@example.test", Username: "somebody"},
			want: store.ErrEmailTaken,
		},
		"same email in capitals": {
			in:   store.NewUser{Email: "TAKEN@example.test", Username: "somebody"},
			want: store.ErrEmailTaken,
		},
		"same username": {
			in:   store.NewUser{Email: "other@example.test", Username: "taken"},
			want: store.ErrUsernameTaken,
		},
		"same username in capitals": {
			in:   store.NewUser{Email: "other@example.test", Username: "TAKEN"},
			want: store.ErrUsernameTaken,
		},
	} {
		t.Run(name, func(t *testing.T) {
			tc.in.PasswordHash = "argon2id$placeholder"

			if _, err := db.CreateUser(ctx, tc.in); !errors.Is(err, tc.want) {
				t.Errorf("CreateUser = %v, want %v", err, tc.want)
			}
		})
	}
}

// The rating insert happens after the user insert, so a registration that
// fails must take the whole thing with it rather than leaving a user nobody
// can rank or a rating with no owner.
func TestARefusedRegistrationLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	if _, err := db.CreateUser(ctx, newUser("first")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	before := counts(t, db)

	clash := newUser("second")
	clash.Email = "first@example.test"
	if _, err := db.CreateUser(ctx, clash); !errors.Is(err, store.ErrEmailTaken) {
		t.Fatalf("CreateUser = %v, want ErrEmailTaken", err)
	}

	if after := counts(t, db); after != before {
		t.Errorf("rows after the failure = %v, want %v", after, before)
	}
}

type rowCounts struct{ users, ratings int }

func counts(t *testing.T, db *store.Store) rowCounts {
	t.Helper()

	var out rowCounts
	const query = `select (select count(*) from users), (select count(*) from ratings)`
	if err := db.Pool().QueryRow(t.Context(), query).Scan(&out.users, &out.ratings); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return out
}

func TestLookupIgnoresTheCaseOfAnEmail(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	want, err := db.CreateUser(ctx, newUser("mixedcase"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := db.UserByEmail(ctx, "MixedCase@Example.Test")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("UserByEmail returned %s, want %s", got.ID, want.ID)
	}
	// Stored as it was typed, not as it was searched for.
	if got.Email != "mixedcase@example.test" {
		t.Errorf("email = %q, want the address as it was registered", got.Email)
	}
}

func TestAMissingUserIsNotFound(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	if _, err := db.UserByID(ctx, id.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UserByID = %v, want ErrNotFound", err)
	}
	if _, err := db.UserByEmail(ctx, "nobody@example.test"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UserByEmail = %v, want ErrNotFound", err)
	}
	if _, err := db.Rating(ctx, id.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Rating = %v, want ErrNotFound", err)
	}
	if _, err := db.CredentialsByEmail(ctx, "nobody@example.test"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CredentialsByEmail = %v, want ErrNotFound", err)
	}
}

func TestSetPasswordHashReplacesWhatIsStored(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	user, err := db.CreateUser(ctx, newUser("rehashed"))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := db.SetPasswordHash(ctx, user.ID, "the new hash"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}

	creds, err := db.CredentialsByEmail(ctx, "rehashed@example.test")
	if err != nil {
		t.Fatalf("CredentialsByEmail: %v", err)
	}
	if creds.Hash != "the new hash" {
		t.Errorf("hash = %q, want the one that was just set", creds.Hash)
	}

	// Setting a hash on nobody is a caller bug, not a silent no-op.
	if err := db.SetPasswordHash(ctx, id.New(), "orphan"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetPasswordHash for an unknown user = %v, want ErrNotFound", err)
	}
}

func TestCredentialsComeBackOnTheirOwn(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()

	in := newUser("loginable")
	in.PasswordHash = "argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"
	user, err := db.CreateUser(ctx, in)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := db.CredentialsByEmail(ctx, "LOGINABLE@example.test")
	if err != nil {
		t.Fatalf("CredentialsByEmail: %v", err)
	}
	if got.UserID != user.ID {
		t.Errorf("credentials are for %s, want %s", got.UserID, user.ID)
	}
	if got.Hash != in.PasswordHash {
		t.Errorf("hash = %q, want the one that was stored", got.Hash)
	}
}

// A hash that has nowhere to sit on the type every handler passes around
// cannot be logged or serialised by accident. This test is the guard on that,
// because the failure it prevents is somebody adding the field later for
// convenience.
func TestAUserHasNowhereToPutAPasswordHash(t *testing.T) {
	t.Parallel()

	forbidden := regexp.MustCompile(`(?i)hash|password|secret|credential|token`)

	typ := reflect.TypeFor[store.User]()
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; forbidden.MatchString(name) {
			t.Errorf("store.User has a field named %q; secrets belong in Credentials", name)
		}
	}
}
