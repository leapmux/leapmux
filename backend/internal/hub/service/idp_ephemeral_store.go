package service

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
)

const (
	// maxOAuthStates limits the in-memory OAuth state store so a flood of
	// anonymous /auth/idp/<provider>/login requests cannot exhaust Hub memory.
	maxOAuthStates = 4096
	// maxPendingOAuthSignups limits the in-memory pending OAuth signup store.
	maxPendingOAuthSignups = 4096
)

var (
	errOAuthStateCapacity         = errors.New("the OAuth state store is at capacity")
	errPendingOAuthSignupCapacity = errors.New("the pending OAuth signup store is at capacity")
)

// expiringStore is a mutex-guarded, capacity-capped in-memory map keyed by an
// opaque string token whose entries expire at a per-entry timestamp and belong
// to an OAuth provider.
type expiringStore[V any] struct {
	mu           sync.Mutex
	entries      map[string]*V
	maxEntries   int
	minExpiresAt time.Time
	capacityErr  error
	now          func() time.Time
	providerID   func(*V) string
	expiresAt    func(*V) time.Time
}

func newExpiringStore[V any](
	maxEntries int,
	capacityErr error,
	providerID func(*V) string,
	expiresAt func(*V) time.Time,
) expiringStore[V] {
	return expiringStore[V]{
		entries:     make(map[string]*V),
		maxEntries:  maxEntries,
		capacityErr: capacityErr,
		now:         time.Now,
		providerID:  providerID,
		expiresAt:   expiresAt,
	}
}

func (s *expiringStore[V]) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *expiringStore[V]) create(key string, entry *V, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; exists {
		return store.ErrConflict
	}
	if len(s.entries) >= s.maxEntries {
		if s.minExpiresAt.IsZero() || s.minExpiresAt.Before(now) {
			s.evictExpiredLocked(now)
		}
		if len(s.entries) >= s.maxEntries {
			return s.capacityErr
		}
	}
	wasEmpty := len(s.entries) == 0
	s.entries[key] = entry
	exp := s.expiresAt(entry)
	if wasEmpty || (!s.minExpiresAt.IsZero() && exp.Before(s.minExpiresAt)) {
		s.minExpiresAt = exp
	}
	return nil
}

func (s *expiringStore[V]) get(key string) (*V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	cp := *entry
	return &cp, true
}

func (s *expiringStore[V]) delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[key]; !ok {
		return false
	}
	delete(s.entries, key)
	if len(s.entries) == 0 {
		s.minExpiresAt = time.Time{}
	}
	return true
}

func (s *expiringStore[V]) deleteByProvider(providerID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for key, entry := range s.entries {
		if s.providerID(entry) == providerID {
			delete(s.entries, key)
			deleted++
		}
	}
	if len(s.entries) == 0 {
		s.minExpiresAt = time.Time{}
	}
	return deleted
}

func (s *expiringStore[V]) deleteExpired(now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked(now.UTC())
}

func (s *expiringStore[V]) evictExpiredLocked(now time.Time) int64 {
	var deleted int64
	var minRem time.Time
	for key, entry := range s.entries {
		exp := s.expiresAt(entry)
		if exp.Before(now) {
			delete(s.entries, key)
			deleted++
			continue
		}
		if minRem.IsZero() || exp.Before(minRem) {
			minRem = exp
		}
	}
	s.minExpiresAt = minRem
	return deleted
}

// OAuthStatePurpose identifies why an inbound OAuth flow started.
type OAuthStatePurpose int

const (
	OAuthStatePurposeUnspecified  OAuthStatePurpose = 0
	OAuthStatePurposeLogin        OAuthStatePurpose = 1
	OAuthStatePurposeReauth       OAuthStatePurpose = 2
	OAuthStatePurposeConsentLogin OAuthStatePurpose = 3
)

// IsValid reports whether p is one of the defined non-zero purposes.
func (p OAuthStatePurpose) IsValid() bool {
	return p >= OAuthStatePurposeLogin && p <= OAuthStatePurposeConsentLogin
}

// String returns a human-readable label for logging and errors.
func (p OAuthStatePurpose) String() string {
	switch p {
	case OAuthStatePurposeLogin:
		return "login"
	case OAuthStatePurposeReauth:
		return "reauth"
	case OAuthStatePurposeConsentLogin:
		return "consent_login"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

// OAuthState holds one short-lived inbound OAuth authorization state.
type OAuthState struct {
	State        string
	ProviderID   string
	PkceVerifier string
	NonceHash    string
	RedirectURI  string
	Purpose      OAuthStatePurpose
	SessionID    string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// CreateOAuthStateParams carries the fields required to insert one OAuth state.
type CreateOAuthStateParams struct {
	State        string
	ProviderID   string
	PkceVerifier string
	NonceHash    string
	RedirectURI  string
	Purpose      OAuthStatePurpose
	SessionID    string
	ExpiresAt    time.Time
}

// OAuthStateStore stores short-lived inbound OAuth states in memory. It is an
// interface so concurrency-barrier and fault-injection tests can wrap it.
type OAuthStateStore interface {
	Create(p CreateOAuthStateParams) error
	Get(state string) (*OAuthState, bool)
	Delete(state string) bool
	DeleteByProvider(providerID string) int64
	DeleteExpired(now time.Time) int64
}

// MemoryOAuthStateStore is the in-memory OAuthStateStore implementation.
type MemoryOAuthStateStore struct {
	expiringStore[OAuthState]
}

// NewMemoryOAuthStateStore returns an empty MemoryOAuthStateStore with the
// default capacity cap.
func NewMemoryOAuthStateStore() *MemoryOAuthStateStore {
	return &MemoryOAuthStateStore{
		expiringStore: newExpiringStore(
			maxOAuthStates,
			errOAuthStateCapacity,
			func(e *OAuthState) string { return e.ProviderID },
			func(e *OAuthState) time.Time { return e.ExpiresAt },
		),
	}
}

func (s *MemoryOAuthStateStore) Create(p CreateOAuthStateParams) error {
	if p.State == "" {
		return fmt.Errorf("oauth state is required: %w", store.ErrInvalidArgument)
	}
	if !p.Purpose.IsValid() {
		return fmt.Errorf("invalid oauth state purpose %d: %w", p.Purpose, store.ErrInvalidArgument)
	}
	if (p.Purpose == OAuthStatePurposeReauth) != (p.SessionID != "") {
		return fmt.Errorf("reauth purpose requires session_id and other purposes forbid it: %w", store.ErrInvalidArgument)
	}

	now := s.currentTime()
	return s.create(p.State, &OAuthState{
		State:        p.State,
		ProviderID:   p.ProviderID,
		PkceVerifier: p.PkceVerifier,
		NonceHash:    p.NonceHash,
		RedirectURI:  p.RedirectURI,
		Purpose:      p.Purpose,
		SessionID:    p.SessionID,
		ExpiresAt:    p.ExpiresAt.UTC(),
		CreatedAt:    now,
	}, now)
}

func (s *MemoryOAuthStateStore) Get(state string) (*OAuthState, bool) {
	return s.get(state)
}

func (s *MemoryOAuthStateStore) Delete(state string) bool {
	return s.delete(state)
}

func (s *MemoryOAuthStateStore) DeleteByProvider(providerID string) int64 {
	return s.deleteByProvider(providerID)
}

func (s *MemoryOAuthStateStore) DeleteExpired(now time.Time) int64 {
	return s.deleteExpired(now)
}

// PendingOAuthSignup holds one short-lived inbound OAuth signup hand-off while
// the user chooses a username on the complete-signup screen.
type PendingOAuthSignup struct {
	Token           string
	ProviderID      string
	NonceHash       string
	ProviderSubject string
	Email           string
	DisplayName     string
	RedirectURI     string
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

// CreatePendingOAuthSignupParams carries the fields required to insert one
// pending OAuth signup.
type CreatePendingOAuthSignupParams struct {
	Token           string
	ProviderID      string
	NonceHash       string
	ProviderSubject string
	Email           string
	DisplayName     string
	RedirectURI     string
	ExpiresAt       time.Time
}

// PendingOAuthSignupStore holds short-lived pending OAuth signups in memory.
type PendingOAuthSignupStore struct {
	expiringStore[PendingOAuthSignup]
}

// NewPendingOAuthSignupStore returns an empty PendingOAuthSignupStore with the
// default capacity cap.
func NewPendingOAuthSignupStore() *PendingOAuthSignupStore {
	return &PendingOAuthSignupStore{
		expiringStore: newExpiringStore(
			maxPendingOAuthSignups,
			errPendingOAuthSignupCapacity,
			func(e *PendingOAuthSignup) string { return e.ProviderID },
			func(e *PendingOAuthSignup) time.Time { return e.ExpiresAt },
		),
	}
}

func (s *PendingOAuthSignupStore) Create(p CreatePendingOAuthSignupParams) error {
	if p.Token == "" {
		return fmt.Errorf("pending signup token is required: %w", store.ErrInvalidArgument)
	}
	now := s.currentTime()
	return s.create(p.Token, &PendingOAuthSignup{
		Token:           p.Token,
		ProviderID:      p.ProviderID,
		NonceHash:       p.NonceHash,
		ProviderSubject: p.ProviderSubject,
		Email:           store.NormalizeEmail(p.Email),
		DisplayName:     p.DisplayName,
		RedirectURI:     p.RedirectURI,
		ExpiresAt:       p.ExpiresAt.UTC(),
		CreatedAt:       now,
	}, now)
}

func (s *PendingOAuthSignupStore) Get(token string) (*PendingOAuthSignup, bool) {
	return s.get(token)
}

func (s *PendingOAuthSignupStore) Delete(token string) bool {
	return s.delete(token)
}

func (s *PendingOAuthSignupStore) DeleteByProvider(providerID string) int64 {
	return s.deleteByProvider(providerID)
}

func (s *PendingOAuthSignupStore) DeleteExpired(now time.Time) int64 {
	return s.deleteExpired(now)
}
