package webauthn

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	gowebauthn "github.com/go-webauthn/webauthn/webauthn"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// maxWebAuthnSessions caps the in-memory ceremony store so anonymous
// BeginSignUp traffic cannot exhaust Hub memory.
const maxWebAuthnSessions = 4096

var errWebAuthnSessionCapacity = errors.New("the WebAuthn ceremony session store is at capacity")

// CeremonySession holds one in-flight WebAuthn ceremony in memory.
type CeremonySession struct {
	ID                 string
	Kind               Kind
	UserID             userid.UserID
	UserAuthGeneration int64
	SessionData        *gowebauthn.SessionData
	SignupDraft        SignupDraft
	TokenHash          string
	ExpiresAt          time.Time
	CreatedAt          time.Time
}

type userKindKey struct {
	userID string
	kind   Kind
}

// SessionStore holds in-flight WebAuthn ceremonies in memory, replacing any
// prior per-user ceremony of the same kind atomically on Create.
type SessionStore struct {
	mu         sync.Mutex
	byID       map[string]*CeremonySession
	byUserKind map[userKindKey]*CeremonySession
	maxEntries int
	now        func() time.Time
}

// NewSessionStore returns an empty SessionStore with the default capacity cap.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		byID:       make(map[string]*CeremonySession),
		byUserKind: make(map[userKindKey]*CeremonySession),
		maxEntries: maxWebAuthnSessions,
		now:        time.Now,
	}
}

func (s *SessionStore) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// Create stores one ceremony session. When !sess.UserID.IsZero() &&
// sess.Kind != KindSignup, any prior open ceremony for the same (user, kind)
// is atomically replaced so repeated Begin calls cannot accumulate entries.
// KindSignup ceremonies allow concurrent anonymous entries.
func (s *SessionStore) Create(sess CeremonySession) error {
	if sess.ID == "" {
		return fmt.Errorf("webauthn ceremony id is required: %w", store.ErrInvalidArgument)
	}
	if !sess.Kind.IsValid() {
		return fmt.Errorf("invalid webauthn ceremony kind %d: %w", sess.Kind, store.ErrInvalidArgument)
	}
	if sess.SessionData == nil {
		return fmt.Errorf("webauthn session data is required: %w", store.ErrInvalidArgument)
	}

	now := s.currentTime()
	stored := cloneCeremonySession(&sess)
	stored.ExpiresAt = stored.ExpiresAt.UTC()
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = now
	} else {
		stored.CreatedAt = stored.CreatedAt.UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.byID[stored.ID]; exists {
		return store.ErrConflict
	}

	perUser := !stored.UserID.IsZero() && stored.Kind != KindSignup
	var uk userKindKey
	var previous *CeremonySession
	if perUser {
		uk = userKindKey{userID: stored.UserID.String(), kind: stored.Kind}
		previous = s.byUserKind[uk]
	}

	if previous == nil && len(s.byID) >= s.maxEntries {
		s.evictExpiredLocked(now)
		if len(s.byID) >= s.maxEntries {
			return errWebAuthnSessionCapacity
		}
	}

	if previous != nil {
		delete(s.byID, previous.ID)
	}
	s.byID[stored.ID] = stored
	if perUser {
		s.byUserKind[uk] = stored
	}
	return nil
}

// Get returns a defensive copy of the stored ceremony session without
// consuming it. Intended for tests that inspect ceremony state.
func (s *SessionStore) Get(id string) (*CeremonySession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return cloneCeremonySession(sess), true
}

// Consume verifies the ceremony's kind, optional recovery token hash, and
// expiry BEFORE deleting it so a kind or token mismatch never consumes an
// in-flight ceremony.
func (s *SessionStore) Consume(id string, kind Kind, expectedTokenHash string, now time.Time) (*CeremonySession, error) {
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w", ErrCeremonyInvalid)
	}
	if sess.Kind != kind {
		return nil, fmt.Errorf("%w: wrong kind", ErrCeremonyInvalid)
	}
	if expectedTokenHash != "" && (sess.TokenHash == "" || sess.TokenHash != expectedTokenHash) {
		return nil, fmt.Errorf("%w: recovery token mismatch", ErrCeremonyInvalid)
	}
	if !sess.ExpiresAt.After(nowUTC) {
		return nil, fmt.Errorf("%w", ErrCeremonyInvalid)
	}

	s.deleteLocked(sess)
	return cloneCeremonySession(sess), nil
}

// DeleteByUserAndKind removes the open ceremony for (userID, kind), if any.
func (s *SessionStore) DeleteByUserAndKind(userID string, kind Kind) int64 {
	if userID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	uk := userKindKey{userID: userID, kind: kind}
	sess, ok := s.byUserKind[uk]
	if !ok {
		return 0
	}
	s.deleteLocked(sess)
	return 1
}

// DeleteByUserBeforeGeneration removes every ceremony for userID whose
// recorded UserAuthGeneration is older than userAuthGeneration per
// auth.ShouldEvictForUserGeneration. When userAuthGeneration <= 0, every
// ceremony for userID is removed.
func (s *SessionStore) DeleteByUserBeforeGeneration(userID string, userAuthGeneration int64) int64 {
	if userID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var deleted int64
	for _, sess := range s.byID {
		if sess.UserID.String() == userID && auth.ShouldEvictForUserGeneration(sess.UserAuthGeneration, userAuthGeneration) {
			s.deleteLocked(sess)
			deleted++
		}
	}
	return deleted
}

// DeleteExpired removes every ceremony whose ExpiresAt is strictly before now.
func (s *SessionStore) DeleteExpired(now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked(now.UTC())
}

func (s *SessionStore) evictExpiredLocked(now time.Time) int64 {
	var deleted int64
	for _, sess := range s.byID {
		if sess.ExpiresAt.Before(now) {
			s.deleteLocked(sess)
			deleted++
		}
	}
	return deleted
}

func (s *SessionStore) deleteLocked(sess *CeremonySession) {
	delete(s.byID, sess.ID)
	if !sess.UserID.IsZero() && sess.Kind != KindSignup {
		uk := userKindKey{userID: sess.UserID.String(), kind: sess.Kind}
		if s.byUserKind[uk] == sess {
			delete(s.byUserKind, uk)
		}
	}
}

func cloneCeremonySession(src *CeremonySession) *CeremonySession {
	if src == nil {
		return nil
	}
	cp := *src
	cp.SessionData = cloneSessionData(src.SessionData)
	return &cp
}

func cloneSessionData(src *gowebauthn.SessionData) *gowebauthn.SessionData {
	if src == nil {
		return nil
	}
	cp := *src
	cp.UserID = bytes.Clone(src.UserID)
	if src.AllowedCredentialIDs != nil {
		cp.AllowedCredentialIDs = make([][]byte, len(src.AllowedCredentialIDs))
		for i, credID := range src.AllowedCredentialIDs {
			cp.AllowedCredentialIDs[i] = bytes.Clone(credID)
		}
	}
	cp.CredParams = slices.Clone(src.CredParams)
	cp.Extensions.Requested = slices.Clone(src.Extensions.Requested)
	if src.Extensions.Extra != nil {
		cp.Extensions.Extra = maps.Clone(src.Extensions.Extra)
		for k, v := range cp.Extensions.Extra {
			if b, ok := v.([]byte); ok {
				cp.Extensions.Extra[k] = bytes.Clone(b)
			}
		}
	}
	return &cp
}
