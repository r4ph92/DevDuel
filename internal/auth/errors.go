package auth

import "errors"

// ErrInvalidCredentials is what a failed login says, whether the address is
// unknown or the password is wrong.
//
// One error for both is the whole point. An error that distinguished them
// would turn the login form into a way of asking whether somebody has an
// account here, which is the first half of every credential stuffing run.
// [Service.Login] also spends the same time on both, since a fast rejection
// says "no such account" just as loudly as a message would.
var ErrInvalidCredentials = errors.New("auth: invalid credentials")

// ErrInvalidSession means the token is malformed, unknown, expired or revoked.
var ErrInvalidSession = errors.New("auth: invalid session")

// ErrTaken means the email address or the username is already registered. It
// does not say which, and callers must not guess.
var ErrTaken = errors.New("auth: email or username is taken")

// Validation failures. These are safe to report to whoever is registering,
// since they are about what they just typed rather than about who else exists.
var (
	ErrInvalidEmail     = errors.New("auth: not a usable email address")
	ErrInvalidUsername  = errors.New("auth: not a usable username")
	ErrPasswordTooShort = errors.New("auth: password is too short")
	ErrPasswordTooLong  = errors.New("auth: password is too long")
)

// ErrMismatch means the password did not produce the stored hash.
var ErrMismatch = errors.New("auth: password does not match")

// ErrMalformedHash means a stored hash is not one this package can read, so
// nothing can be concluded from it and no login may pass on it.
var ErrMalformedHash = errors.New("auth: malformed password hash")
