package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/coder/quartz"
)

// The registration-recovery challenge rules: one challenge is usable
// once, expires after registrationChallengeTTL, belongs to exactly one
// committed attempt, and lives in memory only -- a Hub restart forgets
// every outstanding challenge, and the client simply obtains a fresh
// one through its durable attempt record.
const (
	// registrationChallengeTTL is how long one challenge stays usable.
	registrationChallengeTTL = 60 * time.Second
	// maxOutstandingRegistrationChallenges bounds the in-memory store.
	maxOutstandingRegistrationChallenges = 4096
)

// errChallengeCapacity reports a store at its bound; callers surface it
// as ResourceExhausted so a flood of unknown attempts cannot evict
// live ones.
var errChallengeCapacity = errors.New("the registration challenge store is at capacity")

// registrationChallenge is one outstanding, one-use challenge.
type registrationChallenge struct {
	attemptID string
	id        string
	nonce     []byte
	expiresAt time.Time
}

// registrationChallengeStore holds the outstanding challenges. One
// challenge is outstanding per attempt: issuing a new one replaces the
// attempt's previous challenge, so a replayed issuance cannot widen the
// window.
type registrationChallengeStore struct {
	clock quartz.Clock

	mu        sync.Mutex
	byAttempt map[string]*registrationChallenge
	byID      map[string]*registrationChallenge
}

func newRegistrationChallengeStore(clock quartz.Clock) *registrationChallengeStore {
	if clock == nil {
		clock = quartz.NewReal()
	}
	return &registrationChallengeStore{
		clock:     clock,
		byAttempt: make(map[string]*registrationChallenge),
		byID:      make(map[string]*registrationChallenge),
	}
}

// Issue mints one challenge for attemptID, replacing the attempt's
// previous outstanding challenge.
func (s *registrationChallengeStore) Issue(attemptID string) (id string, nonce []byte, expiresAt time.Time, err error) {
	var idBuf [16]byte
	if _, err = rand.Read(idBuf[:]); err != nil {
		return "", nil, time.Time{}, err
	}
	nonce = make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return "", nil, time.Time{}, err
	}
	id = hex.EncodeToString(idBuf[:])
	now := s.clock.Now()
	expiresAt = now.Add(registrationChallengeTTL)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(now)
	if len(s.byID) >= maxOutstandingRegistrationChallenges {
		return "", nil, time.Time{}, errChallengeCapacity
	}
	challenge := &registrationChallenge{attemptID: attemptID, id: id, nonce: nonce, expiresAt: expiresAt}
	if previous, ok := s.byAttempt[attemptID]; ok {
		delete(s.byID, previous.id)
	}
	s.byAttempt[attemptID] = challenge
	s.byID[id] = challenge
	return id, nonce, expiresAt, nil
}

// Consume takes the challenge for one recovery attempt. It fails when
// the identity is unknown, belongs to another attempt, already served,
// or expired; a consumed challenge is gone for good.
func (s *registrationChallengeStore) Consume(id, attemptID string) ([]byte, bool) {
	now := s.clock.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(now)
	challenge, ok := s.byID[id]
	if !ok || challenge.attemptID != attemptID {
		return nil, false
	}
	nonce := challenge.nonce
	delete(s.byID, challenge.id)
	delete(s.byAttempt, challenge.attemptID)
	return nonce, true
}

// evictExpiredLocked drops expired challenges; the caller holds mu.
func (s *registrationChallengeStore) evictExpiredLocked(now time.Time) {
	for _, challenge := range s.byID {
		if !now.Before(challenge.expiresAt) {
			delete(s.byID, challenge.id)
			if s.byAttempt[challenge.attemptID] == challenge {
				delete(s.byAttempt, challenge.attemptID)
			}
		}
	}
}

// ResetChallengeStore replaces the in-memory challenge store, the state
// a Hub restart loses. Tests use it to prove a client recovers through
// its durable attempt record without any surviving challenge.
func (s *WorkerConnectorService) ResetChallengeStore() {
	s.challenges = newRegistrationChallengeStore(s.clock)
}
