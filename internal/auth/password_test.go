package auth

import (
	"errors"
	"strings"
	"testing"
)

// cheap is the real algorithm at a cost a test can afford. Every property
// these tests check is independent of the parameters, and the parameters
// themselves are checked by reading them back out of the encoding.
var cheap = Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func hashCheaply(t *testing.T, password string) string {
	t.Helper()

	encoded, err := HashWith(cheap, password)
	if err != nil {
		t.Fatalf("HashWith: %v", err)
	}
	return encoded
}

func TestVerifyAcceptsThePasswordThatWasHashed(t *testing.T) {
	encoded := hashCheaply(t, "correct horse battery staple")

	if err := Verify(encoded, "correct horse battery staple"); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerifyRejectsAnythingElse(t *testing.T) {
	encoded := hashCheaply(t, "correct horse battery staple")

	for name, password := range map[string]string{
		"a different password": "incorrect horse battery staple",
		"the empty password":   "",
		"a prefix":             "correct horse battery stapl",
		"a suffix":             "correct horse battery staples",
		"a change of case":     "Correct Horse Battery Staple",
	} {
		t.Run(name, func(t *testing.T) {
			if err := Verify(encoded, password); !errors.Is(err, ErrMismatch) {
				t.Errorf("Verify = %v, want ErrMismatch", err)
			}
		})
	}
}

// Two accounts with the same password must not have the same hash, or the
// database tells an attacker which accounts to attack once.
func TestTheSamePasswordHashesDifferentlyEachTime(t *testing.T) {
	first := hashCheaply(t, "the same password")
	second := hashCheaply(t, "the same password")

	if first == second {
		t.Error("hashing the same password twice produced the same hash")
	}
	if err := Verify(second, "the same password"); err != nil {
		t.Errorf("Verify against the second hash: %v", err)
	}
}

// The stored string is the record of how it was made, which is what lets the
// cost be raised later without invalidating anything.
func TestTheEncodingCarriesItsParameters(t *testing.T) {
	encoded := hashCheaply(t, "a password")

	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("encoded = %q, want the PHC form with the parameters used", encoded)
	}

	p, salt, key, err := decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Memory != cheap.Memory || p.Iterations != cheap.Iterations || p.Parallelism != cheap.Parallelism {
		t.Errorf("decoded params = %+v, want %+v", p, cheap)
	}
	if len(salt) != int(cheap.SaltLength) {
		t.Errorf("salt is %d bytes, want %d", len(salt), cheap.SaltLength)
	}
	if len(key) != int(cheap.KeyLength) {
		t.Errorf("key is %d bytes, want %d", len(key), cheap.KeyLength)
	}
}

// A hash this package cannot read is not a hash a login may pass on.
func TestVerifyRefusesAHashItCannotRead(t *testing.T) {
	good := hashCheaply(t, "a password")

	for name, encoded := range map[string]string{
		"empty":             "",
		"not a hash at all": "hunter2",
		"bcrypt":            "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
		"another argon2":    strings.Replace(good, "argon2id", "argon2i", 1),
		"a future version":  strings.Replace(good, "v=19", "v=20", 1),
		"missing the salt":  strings.Replace(good, "$m=64", "$$m=64", 1),
		"unreadable params": strings.Replace(good, "m=64,t=1,p=1", "m=lots", 1),
		"zero memory":       strings.Replace(good, "m=64", "m=0", 1),
		"outrageous memory": strings.Replace(good, "m=64", "m=99999999", 1),
		"not base64":        good + "!!!",
	} {
		t.Run(name, func(t *testing.T) {
			err := Verify(encoded, "a password")
			if errors.Is(err, ErrMismatch) {
				t.Fatalf("Verify = ErrMismatch, want it to refuse the hash itself")
			}
			if err == nil {
				t.Fatal("Verify accepted a hash it should not be able to read")
			}
		})
	}
}

// A stored hash decides how much memory this process allocates, so a hostile
// one must not be able to ask for all of it.
func TestAStoredHashCannotDemandTheWholeMachine(t *testing.T) {
	greedy := strings.Replace(hashCheaply(t, "a password"), "m=64", "m=4294967295", 1)

	if err := Verify(greedy, "a password"); !errors.Is(err, ErrMalformedHash) {
		t.Errorf("Verify = %v, want ErrMalformedHash", err)
	}
}

func TestHashRefusesAPasswordUsedAsAWeapon(t *testing.T) {
	if _, err := Hash(strings.Repeat("a", MaxPasswordLength+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("Hash = %v, want ErrPasswordTooLong", err)
	}

	// And verifying one costs nothing either, since no stored hash can have
	// come from a password that long.
	encoded := hashCheaply(t, "a password")
	long := strings.Repeat("a", MaxPasswordLength+1)
	if err := Verify(encoded, long); !errors.Is(err, ErrMismatch) {
		t.Errorf("Verify = %v, want ErrMismatch", err)
	}
}

func TestNeedsRehashTracksTheCurrentCost(t *testing.T) {
	if !NeedsRehash(hashCheaply(t, "a password")) {
		t.Error("a hash made at test cost does not need rehashing, want it to")
	}

	current, err := Hash("a password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if NeedsRehash(current) {
		t.Error("a hash made at the current cost needs rehashing, want it not to")
	}
	if !NeedsRehash("not a hash") {
		t.Error("an unreadable hash does not need rehashing, want it to")
	}
}

// The parameters that ship are a security decision, so a change to them
// should be a change to this test as well.
func TestTheDefaultCostIsWhatWasChosen(t *testing.T) {
	if DefaultParams.Memory < 19*1024 {
		t.Errorf("memory = %d KiB, want at least the 19 MiB floor", DefaultParams.Memory)
	}
	if DefaultParams.Iterations < 2 {
		t.Errorf("iterations = %d, want at least 2", DefaultParams.Iterations)
	}
	if DefaultParams.SaltLength < 16 || DefaultParams.KeyLength < 32 {
		t.Errorf("salt %d and key %d bytes, want at least 16 and 32",
			DefaultParams.SaltLength, DefaultParams.KeyLength)
	}
}
