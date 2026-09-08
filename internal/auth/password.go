// Package auth turns a password into something safe to store, and a login
// into a session.
//
// Nothing here ever holds a plaintext password beyond the call it was passed
// to, and nothing outside this package sees a hash: [Service.Register] hands
// the encoded hash straight to the store, and [Service.Login] reads it back
// only to compare against. That is why the store's User type has nowhere to
// put one.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the cost of one hash.
//
// Argon2id's defence is memory: an attacker with a GPU has thousands of cores
// and not thousands of times the memory bandwidth, so Memory is the number
// that matters and Iterations is what tunes the time once memory is fixed.
type Params struct {
	// Memory is the working set in KiB.
	Memory uint32
	// Iterations is the number of passes over that memory.
	Iterations uint32
	// Parallelism is the number of lanes.
	Parallelism uint8
	// SaltLength and KeyLength are in bytes.
	SaltLength uint32
	KeyLength  uint32
}

// DefaultParams is what new passwords are hashed with: 64 MiB over two
// passes, which lands around a tenth of a second on a modern core.
//
// One lane rather than several, because the server's scarce resource is
// whole requests rather than the latency of any one of them, and lanes only
// buy latency. The cost is per login attempt, which is also why login has to
// be rate limited before this is exposed to the internet: 64 MiB times the
// number of simultaneous attempts is a memory budget somebody else controls.
var DefaultParams = Params{
	Memory:      64 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// Password length bounds. The minimum is a policy floor. The maximum exists
// only so that a megabyte of "password" cannot be used to make the server do
// a megabyte of work per request.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 256
)

// maxMemory caps what a stored hash may ask this process to allocate: one
// gibibyte, which is far above any parameters worth using and far below the
// amount that would take the server down.
const maxMemory = 1024 * 1024

// Hash returns the encoded argon2id hash of password, salted freshly.
func Hash(password string) (string, error) { return HashWith(DefaultParams, password) }

// HashWith is Hash with the cost given explicitly, for tests that cannot
// afford the real one.
func HashWith(p Params, password string) (string, error) {
	if len(password) > MaxPasswordLength {
		return "", ErrPasswordTooLong
	}

	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	return encode(p, salt, key), nil
}

// Verify reports whether password produced encoded, as [ErrMismatch] when it
// did not.
//
// The parameters come out of the stored string rather than from
// [DefaultParams], so that raising the cost later leaves every existing hash
// verifiable. A stored hash is a record of how it was made.
func Verify(encoded, password string) error {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return err
	}
	if len(password) > MaxPasswordLength {
		return ErrMismatch
	}

	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)

	// Constant time, so that the comparison does not leak how much of the
	// hash an attacker guessed.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// NeedsRehash reports whether a stored hash was made with less cost than
// [DefaultParams] asks for now, which is the signal to hash the password
// again while the login still has it in hand.
func NeedsRehash(encoded string) bool {
	p, _, _, err := decode(encoded)
	if err != nil {
		// Unreadable is worse than out of date, and rehashing is how it gets
		// replaced.
		return true
	}
	return p.Memory < DefaultParams.Memory ||
		p.Iterations < DefaultParams.Iterations ||
		p.KeyLength < DefaultParams.KeyLength
}

// b64 is the unpadded alphabet the PHC string format uses.
var b64 = base64.RawStdEncoding

// encode writes the PHC string format, the same shape every other argon2
// implementation reads, so a hash is not locked to this program.
func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

// decode reads what encode wrote. Anything else is [ErrMalformedHash]: a hash
// this package cannot read is not a hash a login may be allowed to pass.
func decode(encoded string) (p Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, ErrMalformedHash
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("%w: version %d", ErrMalformedHash, version)
	}

	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism)
	if err != nil {
		return p, nil, nil, ErrMalformedHash
	}
	// A stored hash decides how much memory this process is about to
	// allocate, so a corrupted or hostile one must not be able to ask for all
	// of it.
	if p.Memory == 0 || p.Memory > maxMemory || p.Iterations == 0 || p.Parallelism == 0 {
		return p, nil, nil, ErrMalformedHash
	}

	if salt, err = b64.DecodeString(parts[4]); err != nil || len(salt) == 0 {
		return p, nil, nil, ErrMalformedHash
	}
	if key, err = b64.DecodeString(parts[5]); err != nil || len(key) == 0 {
		return p, nil, nil, ErrMalformedHash
	}

	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	return p, salt, key, nil
}
