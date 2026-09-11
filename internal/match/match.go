// Package match coordinates the lobbies players meet in.
//
// A lobby is a match that has not started: two seats, a shareable code, and
// nothing else. Starting the clock and running the match belong to the state
// machine, and the challenge a lobby was opened for is deliberately not part
// of anything this package hands out, so that waiting in a lobby is not a way
// to read the challenge early.
package match

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
)

// ErrInvalidCode means a join code is not in the shape a code takes, so no
// lobby could have it. It is separate from "no such lobby" because one is the
// caller mistyping and the other is a lobby that has gone.
var ErrInvalidCode = errors.New("match: invalid join code")

// ErrNoChallenges means the service was built with an empty catalog and has
// nothing to open a lobby for.
var ErrNoChallenges = errors.New("match: no challenges available")

// codeAlphabet leaves out the letters and digits that are read back wrongly
// over a voice call or a blurry screenshot: I, O, 0 and 1.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// codeLength gives 32^8, around 40 bits. Codes are recycled once a match
// starts, so only the lobbies open at one moment are candidates for a guess.
const codeLength = 8

// codeAttempts is how many times a collision is redrawn before giving up. A
// collision means the code is held by one of the lobbies open right now, so
// two in a row is already improbable and five is a formality.
const codeAttempts = 5

// Service opens and closes lobbies over the challenges it was given.
type Service struct {
	db *store.Store
	// keys is a snapshot of the catalog's identities, taken at construction.
	// The catalog is immutable and registered before the service exists.
	keys []challenge.Key
}

// NewService returns a service that draws lobbies from catalog.
func NewService(db *store.Store, catalog []*challenge.Spec) *Service {
	keys := make([]challenge.Key, len(catalog))
	for i, spec := range catalog {
		keys[i] = spec.Key()
	}
	return &Service{db: db, keys: keys}
}

// Create opens a lobby for host on a randomly chosen challenge.
//
// A host already in an unfinished match gets [store.ErrInUnfinishedMatch]
// rather than a second lobby, and [Service.Current] is how a client that lost
// the response finds the lobby it already has.
func (s *Service) Create(ctx context.Context, host id.ID) (store.Match, error) {
	if len(s.keys) == 0 {
		return store.Match{}, ErrNoChallenges
	}

	key, err := s.randomChallenge()
	if err != nil {
		return store.Match{}, err
	}

	for range codeAttempts {
		code, err := newCode()
		if err != nil {
			return store.Match{}, err
		}

		match, err := s.db.CreateLobby(ctx, host, key, code)
		if !errors.Is(err, store.ErrLobbyCodeTaken) {
			return match, err
		}
	}
	return store.Match{}, errors.New("match: could not draw an unused join code")
}

// Join seats user in the lobby that code names. Codes are compared without
// case or surrounding whitespace, so one read off a screen and pasted with a
// stray space still works.
func (s *Service) Join(ctx context.Context, user id.ID, code string) (store.Match, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != codeLength {
		return store.Match{}, ErrInvalidCode
	}
	for _, c := range code {
		if !strings.ContainsRune(codeAlphabet, c) {
			return store.Match{}, ErrInvalidCode
		}
	}
	return s.db.JoinLobby(ctx, user, code)
}

// Get returns the match only to a player in it. Anyone else is told it does
// not exist.
func (s *Service) Get(ctx context.Context, match, user id.ID) (store.Match, error) {
	return s.db.MatchForPlayer(ctx, match, user)
}

// Current is the unfinished match the user is in, after a reload or a lost
// response, or [store.ErrNotFound].
func (s *Service) Current(ctx context.Context, user id.ID) (store.Match, error) {
	return s.db.CurrentMatch(ctx, user)
}

// Leave cancels a lobby that has not started, releasing both players. Either
// player may do it: there is nothing to salvage in a lobby of one.
func (s *Service) Leave(ctx context.Context, match, user id.ID) error {
	return s.db.CancelLobby(ctx, match, user)
}

// Ready marks user ready, and starts the match once both players are. The
// bool is true only for the call that started it.
func (s *Service) Ready(ctx context.Context, match, user id.ID) (store.Match, bool, error) {
	return s.db.ReadyUp(ctx, match, user)
}

// Submit records that user is done, and moves the match to judging once both
// players are. The bool is true only for the call that moved it.
func (s *Service) Submit(ctx context.Context, match, user id.ID) (store.Match, bool, error) {
	return s.db.Submit(ctx, match, user)
}

// Expire moves a match past its deadline to judging. It answers to the
// deadline finalizer rather than to a player, which is why it takes no user:
// running out of time is not something either of them does.
func (s *Service) Expire(ctx context.Context, match id.ID) (bool, error) {
	return s.db.Expire(ctx, match)
}

// newCode draws a join code.
//
// The alphabet has 32 symbols and a byte has 256 values, so the remainder is
// exactly eight whole turns of the alphabet and every symbol is equally
// likely. An alphabet whose size did not divide 256 would need rejection
// sampling instead.
func newCode() (string, error) {
	var random [codeLength]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	for i, b := range random {
		random[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(random[:]), nil
}

// randomChallenge picks the challenge a new lobby is opened for. Which one it
// is stays hidden until the match starts, so it is drawn from the same source
// as the join code rather than from a predictable one.
func (s *Service) randomChallenge() (challenge.Key, error) {
	choice, err := rand.Int(rand.Reader, big.NewInt(int64(len(s.keys))))
	if err != nil {
		return challenge.Key{}, err
	}
	return s.keys[choice.Int64()], nil
}
