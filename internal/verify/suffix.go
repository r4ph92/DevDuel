package verify

import (
	"crypto/rand"
	"encoding/hex"
)

// randomSuffix keeps one verification's containers from colliding with
// another's, since both would otherwise name themselves after the challenge.
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("verify: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
