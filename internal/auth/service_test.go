package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/auth"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// These run against the real cost rather than a reduced one. The parameters
// are a security decision, and a test suite that never pays for them is a
// test suite that would not notice them being turned off.

func newService(t *testing.T) (*auth.Service, *store.Store) {
	t.Helper()

	db := storetest.New(t)
	return auth.NewService(db), db
}

const goodPassword = "correct horse battery staple"

func register(t *testing.T, s *auth.Service, name string) store.User {
	t.Helper()

	user, err := s.Register(t.Context(), auth.Registration{
		Email:    name + "@example.test",
		Username: name,
		Password: goodPassword,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return user
}

func TestRegisterOpensAnAccount(t *testing.T) {
	t.Parallel()
	s, db := newService(t)

	user := register(t, s, "newcomer")
	if user.Username != "newcomer" {
		t.Errorf("username = %q, want newcomer", user.Username)
	}

	// The rating comes with it, since Register goes through CreateUser.
	if _, err := db.Rating(t.Context(), user.ID); err != nil {
		t.Errorf("Rating: %v", err)
	}

	// And the password is not what was stored.
	creds, err := db.CredentialsByEmail(t.Context(), "newcomer@example.test")
	if err != nil {
		t.Fatalf("CredentialsByEmail: %v", err)
	}
	if strings.Contains(creds.Hash, goodPassword) {
		t.Error("the stored hash contains the password")
	}
	if err := auth.Verify(creds.Hash, goodPassword); err != nil {
		t.Errorf("the stored hash does not verify the password: %v", err)
	}
}

// The store knows whether it was the address or the name. The caller must
// not, or the registration form becomes a way of asking who has an account.
func TestRegisterWillNotSayWhichDetailWasTaken(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	ctx := t.Context()

	register(t, s, "already")

	for name, in := range map[string]auth.Registration{
		"the same email":    {Email: "already@example.test", Username: "somebodyelse", Password: goodPassword},
		"the same username": {Email: "somebodyelse@example.test", Username: "already", Password: goodPassword},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Register(ctx, in)
			if !errors.Is(err, auth.ErrTaken) {
				t.Fatalf("Register = %v, want ErrTaken", err)
			}
			if errors.Is(err, store.ErrEmailTaken) || errors.Is(err, store.ErrUsernameTaken) {
				t.Error("the error says which detail collided, and must not")
			}
		})
	}
}

func TestRegisterChecksWhatItWasGiven(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	ctx := t.Context()

	for name, tc := range map[string]struct {
		in   auth.Registration
		want error
	}{
		"no email":          {auth.Registration{Username: "someone", Password: goodPassword}, auth.ErrInvalidEmail},
		"not an address":    {auth.Registration{Email: "someone", Username: "someone", Password: goodPassword}, auth.ErrInvalidEmail},
		"a display name":    {auth.Registration{Email: "A <a@b.test>", Username: "someone", Password: goodPassword}, auth.ErrInvalidEmail},
		"no username":       {auth.Registration{Email: "a@b.test", Password: goodPassword}, auth.ErrInvalidUsername},
		"a short username":  {auth.Registration{Email: "a@b.test", Username: "ab", Password: goodPassword}, auth.ErrInvalidUsername},
		"a long username":   {auth.Registration{Email: "a@b.test", Username: strings.Repeat("a", 33), Password: goodPassword}, auth.ErrInvalidUsername},
		"a spaced username": {auth.Registration{Email: "a@b.test", Username: "two words", Password: goodPassword}, auth.ErrInvalidUsername},
		"a short password":  {auth.Registration{Email: "a@b.test", Username: "someone", Password: "short"}, auth.ErrPasswordTooShort},
		"a long password":   {auth.Registration{Email: "a@b.test", Username: "someone", Password: strings.Repeat("a", 257)}, auth.ErrPasswordTooLong},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Register(ctx, tc.in); !errors.Is(err, tc.want) {
				t.Errorf("Register = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLoginOpensASessionThatFindsItsUser(t *testing.T) {
	t.Parallel()
	s, db := newService(t)
	ctx := t.Context()

	want := register(t, s, "returning")

	session, got, err := s.Login(ctx, "returning@example.test", goodPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("Login returned %s, want %s", got.ID, want.ID)
	}
	if session.Token == "" {
		t.Fatal("Login returned an empty token")
	}
	if session.ExpiresAt.Before(time.Now()) {
		t.Errorf("the session expires at %s, which has passed", session.ExpiresAt)
	}

	user, err := db.SessionUser(ctx, auth.Digest(session.Token))
	if err != nil {
		t.Fatalf("SessionUser: %v", err)
	}
	if user.ID != want.ID {
		t.Errorf("the session belongs to %s, want %s", user.ID, want.ID)
	}
}

// The token is the secret. What the database holds must not be usable as one.
func TestTheStoredSessionIsNotTheToken(t *testing.T) {
	t.Parallel()
	s, db := newService(t)
	ctx := t.Context()

	register(t, s, "hashed")
	session, _, err := s.Login(ctx, "hashed@example.test", goodPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	var stored []byte
	const query = `select token_hash from sessions`
	if err := db.Pool().QueryRow(ctx, query).Scan(&stored); err != nil {
		t.Fatalf("read the session: %v", err)
	}
	if string(stored) == session.Token {
		t.Error("the database holds the token itself")
	}
	if len(stored) != 32 {
		t.Errorf("the stored digest is %d bytes, want 32", len(stored))
	}
}

func TestLoginTellsTheTwoFailuresApartToNobody(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	ctx := t.Context()

	register(t, s, "known")

	_, _, wrong := s.Login(ctx, "known@example.test", "not the password")
	_, _, unknown := s.Login(ctx, "nobody@example.test", goodPassword)

	if !errors.Is(wrong, auth.ErrInvalidCredentials) {
		t.Errorf("a wrong password = %v, want ErrInvalidCredentials", wrong)
	}
	if !errors.Is(unknown, auth.ErrInvalidCredentials) {
		t.Errorf("an unknown address = %v, want ErrInvalidCredentials", unknown)
	}
	if wrong.Error() != unknown.Error() {
		t.Errorf("the two failures read differently: %q and %q", wrong, unknown)
	}
}

// The message being identical is only half of it. A login for an address that
// does not exist has to cost what a real one costs, or the clock says what the
// message will not.
func TestLoginIsNotFasterForAnAddressThatDoesNotExist(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	ctx := t.Context()

	register(t, s, "timed")

	start := time.Now()
	_, _, _ = s.Login(ctx, "timed@example.test", "not the password")
	wrongPassword := time.Since(start)

	start = time.Now()
	_, _, _ = s.Login(ctx, "nobody@example.test", goodPassword)
	unknownAddress := time.Since(start)

	// Generous, because this runs on shared CI hardware. The failure it is
	// looking for is the decoy verification being dropped, which would make
	// the unknown address hundreds of times faster rather than twice.
	if floor := wrongPassword / 4; unknownAddress < floor {
		t.Errorf("an unknown address took %s and a wrong password %s: the decoy hash is not being run",
			unknownAddress, wrongPassword)
	}
}

// Raising the cost has to reach the accounts that were hashed before it, and
// a login is the only moment the password is in hand to do that with.
func TestLoginUpgradesAHashMadeMoreCheaply(t *testing.T) {
	t.Parallel()
	s, db := newService(t)
	ctx := t.Context()

	weak := auth.Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	old, err := auth.HashWith(weak, goodPassword)
	if err != nil {
		t.Fatalf("HashWith: %v", err)
	}

	user, err := db.CreateUser(ctx, store.NewUser{
		Email:        "old@example.test",
		Username:     "old",
		PasswordHash: old,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, _, err := s.Login(ctx, "old@example.test", goodPassword); err != nil {
		t.Fatalf("Login: %v", err)
	}

	creds, err := db.CredentialsByEmail(ctx, "old@example.test")
	if err != nil {
		t.Fatalf("CredentialsByEmail: %v", err)
	}
	if creds.UserID != user.ID {
		t.Fatalf("credentials are for %s, want %s", creds.UserID, user.ID)
	}
	if creds.Hash == old {
		t.Error("the weak hash is still stored after a successful login")
	}
	if auth.NeedsRehash(creds.Hash) {
		t.Error("the upgraded hash still needs rehashing")
	}
	// And the upgrade did not lock anybody out.
	if err := auth.Verify(creds.Hash, goodPassword); err != nil {
		t.Errorf("the upgraded hash does not verify the password: %v", err)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	t.Parallel()
	s, db := newService(t)
	ctx := t.Context()

	register(t, s, "leaving")
	session, _, err := s.Login(ctx, "leaving@example.test", goodPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if err := s.Logout(ctx, session.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := db.SessionUser(ctx, auth.Digest(session.Token)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionUser after logout = %v, want ErrNotFound", err)
	}

	// A stale tab signing out again is not an error.
	if err := s.Logout(ctx, session.Token); err != nil {
		t.Errorf("Logout twice: %v", err)
	}
}
