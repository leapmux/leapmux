package service

import (
	"context"
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
	Create(ctx context.Context, p CreateOAuthStateParams) error
	Get(ctx context.Context, state string) (*OAuthState, error)
	Delete(ctx context.Context, state string) (int64, error)
	DeleteByProvider(providerID string) int64
	DeleteExpired(now time.Time) int64
}

// MemoryOAuthStateStore is the in-memory OAuthStateStore implementation.
type MemoryOAuthStateStore struct {
	mu         sync.Mutex
	entries    map[string]*OAuthState
	maxEntries int
	now        func() time.Time
}

// NewMemoryOAuthStateStore returns an empty MemoryOAuthStateStore with the
// default capacity cap.
func NewMemoryOAuthStateStore() *MemoryOAuthStateStore {
	return &MemoryOAuthStateStore{
		entries:    make(map[string]*OAuthState),
		maxEntries: maxOAuthStates,
		now:        time.Now,
	}
}

func (s *MemoryOAuthStateStore) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *MemoryOAuthStateStore) Create(_ context.Context, p CreateOAuthStateParams) error {
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

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[p.State]; exists {
		return store.ErrConflict
	}
	if len(s.entries) >= s.maxEntries {
		s.evictExpiredLocked(now)
		if len(s.entries) >= s.maxEntries {
			return errOAuthStateCapacity
		}
	}
	s.entries[p.State] = &OAuthState{
		State:        p.State,
		ProviderID:   p.ProviderID,
		PkceVerifier: p.PkceVerifier,
		NonceHash:    p.NonceHash,
		RedirectURI:  p.RedirectURI,
		Purpose:      p.Purpose,
		SessionID:    p.SessionID,
		ExpiresAt:    p.ExpiresAt.UTC(),
		CreatedAt:    now,
	}
	return nil
}

func (s *MemoryOAuthStateStore) Get(_ context.Context, state string) (*OAuthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[state]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *entry
	return &cp, nil
}

func (s *MemoryOAuthStateStore) Delete(_ context.Context, state string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[state]; !ok {
		return 0, nil
	}
	delete(s.entries, state)
	return 1, nil
}

func (s *MemoryOAuthStateStore) DeleteByProvider(providerID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for key, entry := range s.entries {
		if entry.ProviderID == providerID {
			delete(s.entries, key)
			deleted++
		}
	}
	return deleted
}

func (s *MemoryOAuthStateStore) DeleteExpired(now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked(now.UTC())
}

func (s *MemoryOAuthStateStore) evictExpiredLocked(now time.Time) int64 {
	var deleted int64
	for key, entry := range s.entries {
		if entry.ExpiresAt.Before(now) {
			delete(s.entries, key)
			deleted++
		}
	}
	return deleted
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
	mu         sync.Mutex
	entries    map[string]*PendingOAuthSignup
	maxEntries int
	now        func() time.Time
}

// NewPendingOAuthSignupStore returns an empty PendingOAuthSignupStore with the
// default capacity cap.
func NewPendingOAuthSignupStore() *PendingOAuthSignupStore {
	return &PendingOAuthSignupStore{
		entries:    make(map[string]*PendingOAuthSignup),
		maxEntries: maxPendingOAuthSignups,
		now:        time.Now,
	}
}

func (s *PendingOAuthSignupStore) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *PendingOAuthSignupStore) Create(_ context.Context, p CreatePendingOAuthSignupParams) error {
	if p.Token == "" {
		return fmt.Errorf("pending signup token is required: %w", store.ErrInvalidArgument)
	}
	now := s.currentTime()

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[p.Token]; exists {
		return store.ErrConflict
	}
	if len(s.entries) >= s.maxEntries {
		s.evictExpiredLocked(now)
		if len(s.entries) >= s.maxEntries {
			return errPendingOAuthSignupCapacity
		}
	}
	s.entries[p.Token] = &PendingOAuthSignup{
		Token:           p.Token,
		ProviderID:      p.ProviderID,
		NonceHash:       p.NonceHash,
		ProviderSubject: p.ProviderSubject,
		Email:           store.NormalizeEmail(p.Email),
		DisplayName:     p.DisplayName,
		RedirectURI:     p.RedirectURI,
		ExpiresAt:       p.ExpiresAt.UTC(),
		CreatedAt:       now,
	}
	return nil
}

func (s *PendingOAuthSignupStore) Get(_ context.Context, token string) (*PendingOAuthSignup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[token]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *entry
	return &cp, nil
}

func (s *PendingOAuthSignupStore) Delete(_ context.Context, token string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[token]; !ok {
		return 0, nil
	}
	delete(s.entries, token)
	return 1, nil
}

func (s *PendingOAuthSignupStore) DeleteByProvider(providerID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for key, entry := range s.entries {
		if entry.ProviderID == providerID {
			delete(s.entries, key)
			deleted++
		}
	}
	return deleted
}

func (s *PendingOAuthSignupStore) DeleteExpired(now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked(now.UTC())
}

func (s *PendingOAuthSignupStore) evictExpiredLocked(now time.Time) int64 {
	var deleted int64
	for key, entry := range s.entries {
		if entry.ExpiresAt.Before(now) {
			delete(s.entries, key)
			deleted++
		}
	}
	return deleted
}
