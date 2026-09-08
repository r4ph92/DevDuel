package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
)

// SessionLifetime is how long a login lasts. Long enough that a player is not
// signed out between evenings, short enough that a session nobody uses again
// stops being worth stealing.
const SessionLifetime = 30 * 24 * time.Hour

// tokenBytes is the size of a session token before encoding. 256 bits from a
// cryptographic source, which is why the database can store a plain SHA-256
// of it rather than a password hash.
const tokenBytes = 32

// Service registers accounts and turns logins into sessions.
type Service struct {
	db       *store.Store
	lifetime time.Duration
}

// NewService returns a Service over db.
func NewService(db *store.Store) *Service {
	// Built ahead of the login that first needs it. Building it costs exactly
	// one password hash, and paying that inside the first failed login for an
	// unknown address would make that one request measurably slower than
	// every other failure, which is the sort of difference the decoy exists
	// to remove.
	go decoyHash()

	return &Service{db: db, lifetime: SessionLifetime}
}

// Registration is what somebody typed into the sign up form.
type Registration struct {
	Email    string
	Username string
	Password string
}

// Session is a login. Token is the only time the secret exists outside the
// client: the database keeps its digest, so it cannot be recovered from here.
type Session struct {
	Token     string
	ExpiresAt time.Time
}

// Register creates an account and its rating.
//
// It reports [ErrTaken] without saying whether it was the address or the name
// that collided. The store knows which, and says so to the logs, but the
// caller does not: a registration form that confirms an address is registered
// is an account oracle with a friendly message.
func (s *Service) Register(ctx context.Context, in Registration) (store.User, error) {
	email, err := normaliseEmail(in.Email)
	if err != nil {
		return store.User{}, err
	}
	username, err := checkUsername(in.Username)
	if err != nil {
		return store.User{}, err
	}
	if err := CheckPassword(in.Password); err != nil {
		return store.User{}, err
	}

	hash, err := Hash(in.Password)
	if err != nil {
		return store.User{}, err
	}

	user, err := s.db.CreateUser(ctx, store.NewUser{
		Email:        email,
		Username:     username,
		PasswordHash: hash,
	})
	switch {
	case errors.Is(err, store.ErrEmailTaken), errors.Is(err, store.ErrUsernameTaken):
		return store.User{}, ErrTaken
	case err != nil:
		return store.User{}, err
	}
	return user, nil
}

// Login checks a password and opens a session.
//
// An unknown address and a wrong password are the same answer and, as far as
// anyone timing the response can tell, the same amount of work: when no
// account matches, the password is still verified, against a hash of
// something nobody knows. Skipping that would make a login for an address
// that does not exist measurably faster than one for an address that does.
func (s *Service) Login(ctx context.Context, email, password string) (Session, store.User, error) {
	creds, err := s.db.CredentialsByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, store.ErrNotFound) {
		_ = Verify(decoyHash(), password)
		return Session{}, store.User{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, store.User{}, err
	}

	if err := Verify(creds.Hash, password); err != nil {
		return Session{}, store.User{}, ErrInvalidCredentials
	}
	s.rehash(ctx, creds, password)

	// A second query rather than one that returns the account and the hash
	// together, because a row that carries both is a struct that carries
	// both, and that is the thing this design is avoiding. It is a primary
	// key lookup next to an argon2 hash, so it costs nothing worth having.
	user, err := s.db.UserByID(ctx, creds.UserID)
	if err != nil {
		return Session{}, store.User{}, err
	}

	session, err := s.open(ctx, creds.UserID)
	if err != nil {
		return Session{}, store.User{}, err
	}
	return session, user, nil
}

// rehash upgrades a stored hash that was made with less cost than is asked
// for now, which is the only moment the password is in hand to do it with.
//
// Best effort on purpose: the login already succeeded, and failing it because
// an optimisation did not work would be a strange thing to do to somebody who
// typed their password correctly.
func (s *Service) rehash(ctx context.Context, creds store.Credentials, password string) {
	if !NeedsRehash(creds.Hash) {
		return
	}

	upgraded, err := Hash(password)
	if err != nil {
		return
	}
	_ = s.db.SetPasswordHash(ctx, creds.UserID, upgraded)
}

// Authenticate resolves a session without renewing it. Only the canonical
// encoding minted by Login is accepted; the raw token never reaches storage.
func (s *Service) Authenticate(ctx context.Context, token string) (store.User, error) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return store.User{}, ErrInvalidSession
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return store.User{}, ErrInvalidSession
	}
	user, err := s.db.SessionUser(ctx, Digest(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrInvalidSession
	}
	return user, err
}

// Logout ends a session. Ending one that is already gone is not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.db.DeleteSession(ctx, Digest(token))
}

// open mints a token and records its digest.
func (s *Service) open(ctx context.Context, user id.ID) (Session, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("auth: read token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(s.lifetime)
	if err := s.db.CreateSession(ctx, Digest(token), user, expires); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expires}, nil
}

// Digest is how a token becomes the thing the database stores. SHA-256 is
// enough here, unlike for a password: the input is 256 random bits, so there
// is no guessable candidate list to slow an attacker down over.
func Digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// decoyHash is a real hash of a password nobody knows, verified against when
// no account matched so that the failure costs what a real one costs. It is
// built once, on first use, because building it is deliberately expensive.
var decoyHash = sync.OnceValue(func() string {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// Without randomness there is no session token either, so the process
		// is not going to be doing any authentication.
		panic("auth: no randomness available: " + err.Error())
	}

	hash, err := Hash(string(secret))
	if err != nil {
		panic("auth: cannot hash: " + err.Error())
	}
	return hash
})

// CheckPassword applies the length policy. What it does not do is judge the
// contents: composition rules push people towards Passw0rd! and away from
// length, which is the thing that actually helps.
func CheckPassword(password string) error {
	switch {
	case len(password) < MinPasswordLength:
		return ErrPasswordTooShort
	case len(password) > MaxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}

// normaliseEmail trims and checks the address, keeping the case somebody
// typed. The store matches case insensitively, so nothing is gained by
// rewriting somebody's own address at them.
func normaliseEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > 320 {
		return "", ErrInvalidEmail
	}

	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		// ParseAddress accepts `Name <a@b.c>`, which is not an address
		// somebody registers with.
		return "", ErrInvalidEmail
	}
	return email, nil
}

// usernameShape is what a name may look like: it has to be legible in a URL,
// in a match result and next to somebody else's.
var usernameShape = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{2,31}$`)

func checkUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if !usernameShape.MatchString(username) {
		return "", ErrInvalidUsername
	}
	return username, nil
}
