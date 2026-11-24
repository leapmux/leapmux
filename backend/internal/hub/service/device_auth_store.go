package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const (
	// maxDeviceAuthorizations caps the in-memory device authorization store so
	// anonymous /oauth/device-authorization traffic cannot exhaust Hub memory.
	maxDeviceAuthorizations = 4096
	// deviceCodeHashLength is the HMAC-SHA256 digest length required for every
	// stored device authorization.
	deviceCodeHashLength = 32
)

var errDeviceAuthCapacity = errors.New("the device authorization store is at capacity")

// DeviceAuthorizationState tracks one RFC 8628 device grant lifecycle.
type DeviceAuthorizationState int

const (
	DeviceAuthorizationStateUnspecified DeviceAuthorizationState = 0
	DeviceAuthorizationStatePending     DeviceAuthorizationState = 1
	DeviceAuthorizationStateApproved    DeviceAuthorizationState = 2
	DeviceAuthorizationStateDenied      DeviceAuthorizationState = 3
)

// String returns a human-readable label for logging and errors.
func (s DeviceAuthorizationState) String() string {
	switch s {
	case DeviceAuthorizationStatePending:
		return "pending"
	case DeviceAuthorizationStateApproved:
		return "approved"
	case DeviceAuthorizationStateDenied:
		return "denied"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// DeviceAuthorization represents one RFC 8628 device-code or step-up grant in
// memory.
type DeviceAuthorization struct {
	DeviceCodeHash  []byte
	UserCode        string
	DeviceName      string
	ClientID        string
	RequestedScopes string
	GrantedScopes   string
	State           DeviceAuthorizationState
	UserID          string
	AuthGeneration  int64
	ExpiresAt       time.Time
	LastPolledAt    *time.Time
	ConsumedAt      *time.Time
	CreatedAt       time.Time
	ElevateTokenID  string
}

// CreateDeviceAuthorizationParams carries the fields required to insert one
// pending device authorization.
type CreateDeviceAuthorizationParams struct {
	DeviceCodeHash  []byte
	UserCode        string
	DeviceName      string
	ClientID        string
	RequestedScopes string
	ExpiresAt       time.Time
	ElevateTokenID  string
}

// ApproveDeviceAuthorizationParams carries the fields required to approve one
// pending device authorization by user code.
type ApproveDeviceAuthorizationParams struct {
	UserCode       string
	UserID         userid.UserID
	GrantedScopes  string
	AuthGeneration int64
}

// DeviceGrantReservation is an opaque token representing an in-flight
// two-phase consume or step-up approval reservation.
type DeviceGrantReservation struct {
	id  uint64
	key string
}

// DeviceInvalidateToken is an opaque token representing an active two-phase
// invalidation or deletion guard.
type DeviceInvalidateToken struct {
	id uint64
}

// DeviceAuthorizationStore holds short-lived RFC 8628 device grants in memory
// with two-phase reservation and invalidation guards so concurrent SQL
// transactions stay linearizable without holding any mutex across SQL I/O.
type DeviceAuthorizationStore interface {
	Create(ctx context.Context, p CreateDeviceAuthorizationParams) error
	Get(ctx context.Context, deviceCodeHash []byte) (*DeviceAuthorization, error)
	GetByUserCode(ctx context.Context, userCode string) (*DeviceAuthorization, error)
	TouchPoll(ctx context.Context, deviceCodeHash []byte, now time.Time) error
	DenyByUserCode(ctx context.Context, userCode string) (int64, error)
	ApproveByUserCode(ctx context.Context, p ApproveDeviceAuthorizationParams, now time.Time) (int64, error)
	Consume(ctx context.Context, deviceCodeHash []byte, now time.Time) (int64, error)
	DeleteExpired(now time.Time) int64

	// Two-phase token issuance: reserve before SQL transaction, check
	// IsInvalidated inside the transaction, commit or release after.
	ReserveConsume(deviceCodeHash []byte, now time.Time) (DeviceGrantReservation, bool)
	IsInvalidated(res DeviceGrantReservation) bool
	CommitConsume(res DeviceGrantReservation, now time.Time)
	ReleaseConsume(res DeviceGrantReservation)

	// Two-phase step-up approval: reserve before SQL elevation write, commit
	// or release after.
	ReserveApproveByUserCode(userCode string, userID userid.UserID, now time.Time) (DeviceGrantReservation, bool)
	CommitApproveByUserCode(res DeviceGrantReservation, p ApproveDeviceAuthorizationParams)
	ReleaseApproveByUserCode(res DeviceGrantReservation)

	// Two-phase invalidation: begin before SQL transaction, commit or abort
	// after.
	BeginInvalidateForUserClient(clientID string, userID userid.UserID) DeviceInvalidateToken
	BeginInvalidateForClient(clientID string) DeviceInvalidateToken
	BeginDeleteForClient(clientID string) DeviceInvalidateToken
	CommitInvalidate(tok DeviceInvalidateToken, now time.Time)
	AbortInvalidate(tok DeviceInvalidateToken)

	// Generation-selective user revocation (post-commit observer).
	ConsumeApprovedForUserBeforeGeneration(userID string, userAuthGeneration int64, now time.Time) int64
}

type deviceAuthEntry struct {
	key          string
	row          DeviceAuthorization
	consumeResID uint64
	approveResID uint64
}

type consumeReservation struct {
	key            string
	clientID       string
	userID         string
	authGeneration int64
	invalidated    bool
}

type approveReservation struct {
	key      string
	userCode string
	userID   userid.UserID
}

type invalidateMode int

const (
	invalidateModeUserClient invalidateMode = iota + 1
	invalidateModeClient
	invalidateModeDeleteClient
)

type invalidationGuard struct {
	id       uint64
	mode     invalidateMode
	clientID string
	userID   string
}

func (g *invalidationGuard) matches(clientID, userID string) bool {
	if g.clientID != clientID {
		return false
	}
	if g.mode == invalidateModeUserClient {
		return g.userID == userID
	}
	return true
}

// MemoryDeviceAuthorizationStore is the in-memory DeviceAuthorizationStore
// implementation.
type MemoryDeviceAuthorizationStore struct {
	mu            sync.Mutex
	byHash        map[string]*deviceAuthEntry
	byUserCode    map[string]*deviceAuthEntry
	consumeRes    map[uint64]*consumeReservation
	approveRes    map[uint64]*approveReservation
	invalidations map[uint64]*invalidationGuard
	nextID        uint64
	maxEntries    int
	now           func() time.Time
}

// NewMemoryDeviceAuthorizationStore returns an empty
// MemoryDeviceAuthorizationStore with the default capacity cap.
func NewMemoryDeviceAuthorizationStore() *MemoryDeviceAuthorizationStore {
	return &MemoryDeviceAuthorizationStore{
		byHash:        make(map[string]*deviceAuthEntry),
		byUserCode:    make(map[string]*deviceAuthEntry),
		consumeRes:    make(map[uint64]*consumeReservation),
		approveRes:    make(map[uint64]*approveReservation),
		invalidations: make(map[uint64]*invalidationGuard),
		nextID:        1,
		maxEntries:    maxDeviceAuthorizations,
		now:           time.Now,
	}
}

func (s *MemoryDeviceAuthorizationStore) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *MemoryDeviceAuthorizationStore) allocIDLocked() uint64 {
	id := s.nextID
	s.nextID++
	return id
}

func (s *MemoryDeviceAuthorizationStore) hasActiveInvalidationLocked(clientID, userID string) bool {
	for _, g := range s.invalidations {
		if g.matches(clientID, userID) {
			return true
		}
	}
	return false
}

func (s *MemoryDeviceAuthorizationStore) Create(_ context.Context, p CreateDeviceAuthorizationParams) error {
	if len(p.DeviceCodeHash) != deviceCodeHashLength {
		return fmt.Errorf("device_code_hash must be %d bytes: %w", deviceCodeHashLength, store.ErrInvalidArgument)
	}
	if p.UserCode == "" {
		return fmt.Errorf("user_code is required: %w", store.ErrInvalidArgument)
	}
	if p.ClientID == "" {
		return fmt.Errorf("client_id is required: %w", store.ErrInvalidArgument)
	}

	key := string(p.DeviceCodeHash)
	now := s.currentTime()

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.byHash[key]; exists {
		return store.ErrConflict
	}
	if _, exists := s.byUserCode[p.UserCode]; exists {
		return store.ErrConflict
	}
	if len(s.byHash) >= s.maxEntries {
		s.evictExpiredLocked(now)
		if len(s.byHash) >= s.maxEntries {
			return errDeviceAuthCapacity
		}
	}

	entry := &deviceAuthEntry{
		key: key,
		row: DeviceAuthorization{
			DeviceCodeHash:  bytes.Clone(p.DeviceCodeHash),
			UserCode:        p.UserCode,
			DeviceName:      p.DeviceName,
			ClientID:        p.ClientID,
			RequestedScopes: p.RequestedScopes,
			State:           DeviceAuthorizationStatePending,
			ExpiresAt:       p.ExpiresAt.UTC(),
			CreatedAt:       now,
			ElevateTokenID:  p.ElevateTokenID,
		},
	}
	s.byHash[key] = entry
	s.byUserCode[p.UserCode] = entry
	return nil
}

func (s *MemoryDeviceAuthorizationStore) Get(_ context.Context, deviceCodeHash []byte) (*DeviceAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[string(deviceCodeHash)]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneDeviceAuthorization(&entry.row), nil
}

func (s *MemoryDeviceAuthorizationStore) GetByUserCode(_ context.Context, userCode string) (*DeviceAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byUserCode[userCode]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneDeviceAuthorization(&entry.row), nil
}

func (s *MemoryDeviceAuthorizationStore) TouchPoll(_ context.Context, deviceCodeHash []byte, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[string(deviceCodeHash)]
	if !ok {
		return nil
	}
	ts := now.UTC()
	entry.row.LastPolledAt = &ts
	return nil
}

func (s *MemoryDeviceAuthorizationStore) DenyByUserCode(_ context.Context, userCode string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byUserCode[userCode]
	if !ok {
		return 0, nil
	}
	if entry.row.State != DeviceAuthorizationStatePending || entry.row.ConsumedAt != nil || entry.approveResID != 0 {
		return 0, nil
	}
	entry.row.State = DeviceAuthorizationStateDenied
	return 1, nil
}

func (s *MemoryDeviceAuthorizationStore) ApproveByUserCode(_ context.Context, p ApproveDeviceAuthorizationParams, now time.Time) (int64, error) {
	if p.UserID.IsZero() {
		return 0, fmt.Errorf("approve device authorization requires a user id: %w", store.ErrInvalidArgument)
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byUserCode[p.UserCode]
	if !ok {
		return 0, nil
	}
	if entry.row.State != DeviceAuthorizationStatePending ||
		entry.row.ConsumedAt != nil ||
		!entry.row.ExpiresAt.After(nowUTC) ||
		entry.approveResID != 0 {
		return 0, nil
	}
	entry.row.State = DeviceAuthorizationStateApproved
	entry.row.UserID = p.UserID.String()
	entry.row.GrantedScopes = p.GrantedScopes
	entry.row.AuthGeneration = p.AuthGeneration
	return 1, nil
}

func (s *MemoryDeviceAuthorizationStore) Consume(_ context.Context, deviceCodeHash []byte, now time.Time) (int64, error) {
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[string(deviceCodeHash)]
	if !ok {
		return 0, nil
	}
	if entry.row.State != DeviceAuthorizationStateApproved ||
		entry.row.UserID == "" ||
		entry.row.ConsumedAt != nil ||
		!entry.row.ExpiresAt.After(nowUTC) ||
		entry.consumeResID != 0 ||
		entry.approveResID != 0 {
		return 0, nil
	}
	if s.hasActiveInvalidationLocked(entry.row.ClientID, entry.row.UserID) {
		return 0, nil
	}
	ts := nowUTC
	entry.row.ConsumedAt = &ts
	return 1, nil
}

func (s *MemoryDeviceAuthorizationStore) ReserveConsume(deviceCodeHash []byte, now time.Time) (DeviceGrantReservation, bool) {
	nowUTC := now.UTC()
	key := string(deviceCodeHash)

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[key]
	if !ok {
		return DeviceGrantReservation{}, false
	}
	if entry.row.State != DeviceAuthorizationStateApproved ||
		entry.row.UserID == "" ||
		entry.row.ConsumedAt != nil ||
		!entry.row.ExpiresAt.After(nowUTC) ||
		entry.consumeResID != 0 ||
		entry.approveResID != 0 {
		return DeviceGrantReservation{}, false
	}
	if s.hasActiveInvalidationLocked(entry.row.ClientID, entry.row.UserID) {
		return DeviceGrantReservation{}, false
	}

	resID := s.allocIDLocked()
	entry.consumeResID = resID
	s.consumeRes[resID] = &consumeReservation{
		key:            key,
		clientID:       entry.row.ClientID,
		userID:         entry.row.UserID,
		authGeneration: entry.row.AuthGeneration,
	}
	return DeviceGrantReservation{id: resID, key: key}, true
}

func (s *MemoryDeviceAuthorizationStore) IsInvalidated(res DeviceGrantReservation) bool {
	if res.id == 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cr, ok := s.consumeRes[res.id]
	if !ok || cr.invalidated {
		return true
	}
	return s.hasActiveInvalidationLocked(cr.clientID, cr.userID)
}

func (s *MemoryDeviceAuthorizationStore) CommitConsume(res DeviceGrantReservation, now time.Time) {
	if res.id == 0 {
		return
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	cr, ok := s.consumeRes[res.id]
	if !ok {
		return
	}
	delete(s.consumeRes, res.id)
	if entry, exists := s.byHash[cr.key]; exists && entry.consumeResID == res.id {
		entry.consumeResID = 0
		if entry.row.ConsumedAt == nil {
			ts := nowUTC
			entry.row.ConsumedAt = &ts
		}
	}
}

func (s *MemoryDeviceAuthorizationStore) ReleaseConsume(res DeviceGrantReservation) {
	if res.id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cr, ok := s.consumeRes[res.id]
	if !ok {
		return
	}
	delete(s.consumeRes, res.id)
	if entry, exists := s.byHash[cr.key]; exists && entry.consumeResID == res.id {
		entry.consumeResID = 0
		if cr.invalidated && entry.row.ConsumedAt == nil {
			ts := s.currentTime()
			entry.row.ConsumedAt = &ts
		}
	}
}

func (s *MemoryDeviceAuthorizationStore) ReserveApproveByUserCode(userCode string, userID userid.UserID, now time.Time) (DeviceGrantReservation, bool) {
	if userCode == "" || userID.IsZero() {
		return DeviceGrantReservation{}, false
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byUserCode[userCode]
	if !ok {
		return DeviceGrantReservation{}, false
	}
	if entry.row.State != DeviceAuthorizationStatePending ||
		entry.row.ConsumedAt != nil ||
		!entry.row.ExpiresAt.After(nowUTC) ||
		entry.approveResID != 0 ||
		entry.consumeResID != 0 {
		return DeviceGrantReservation{}, false
	}

	resID := s.allocIDLocked()
	entry.approveResID = resID
	s.approveRes[resID] = &approveReservation{
		key:      entry.key,
		userCode: userCode,
		userID:   userID,
	}
	return DeviceGrantReservation{id: resID, key: entry.key}, true
}

func (s *MemoryDeviceAuthorizationStore) CommitApproveByUserCode(res DeviceGrantReservation, p ApproveDeviceAuthorizationParams) {
	if res.id == 0 || p.UserID.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ar, ok := s.approveRes[res.id]
	if !ok {
		return
	}
	delete(s.approveRes, res.id)
	if entry, exists := s.byHash[ar.key]; exists && entry.approveResID == res.id {
		entry.approveResID = 0
		if entry.row.State == DeviceAuthorizationStatePending && entry.row.ConsumedAt == nil {
			entry.row.State = DeviceAuthorizationStateApproved
			entry.row.UserID = p.UserID.String()
			entry.row.GrantedScopes = p.GrantedScopes
			entry.row.AuthGeneration = p.AuthGeneration
		}
	}
}

func (s *MemoryDeviceAuthorizationStore) ReleaseApproveByUserCode(res DeviceGrantReservation) {
	if res.id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ar, ok := s.approveRes[res.id]
	if !ok {
		return
	}
	delete(s.approveRes, res.id)
	if entry, exists := s.byHash[ar.key]; exists && entry.approveResID == res.id {
		entry.approveResID = 0
	}
}

func (s *MemoryDeviceAuthorizationStore) BeginInvalidateForUserClient(clientID string, userID userid.UserID) DeviceInvalidateToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.allocIDLocked()
	s.invalidations[id] = &invalidationGuard{
		id:       id,
		mode:     invalidateModeUserClient,
		clientID: clientID,
		userID:   userID.String(),
	}
	return DeviceInvalidateToken{id: id}
}

func (s *MemoryDeviceAuthorizationStore) BeginInvalidateForClient(clientID string) DeviceInvalidateToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.allocIDLocked()
	s.invalidations[id] = &invalidationGuard{
		id:       id,
		mode:     invalidateModeClient,
		clientID: clientID,
	}
	return DeviceInvalidateToken{id: id}
}

func (s *MemoryDeviceAuthorizationStore) BeginDeleteForClient(clientID string) DeviceInvalidateToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.allocIDLocked()
	s.invalidations[id] = &invalidationGuard{
		id:       id,
		mode:     invalidateModeDeleteClient,
		clientID: clientID,
	}
	return DeviceInvalidateToken{id: id}
}

func (s *MemoryDeviceAuthorizationStore) CommitInvalidate(tok DeviceInvalidateToken, now time.Time) {
	if tok.id == 0 {
		return
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.invalidations[tok.id]
	if !ok {
		return
	}
	delete(s.invalidations, tok.id)

	for _, cr := range s.consumeRes {
		if g.matches(cr.clientID, cr.userID) {
			cr.invalidated = true
		}
	}

	switch g.mode {
	case invalidateModeUserClient:
		for _, entry := range s.byHash {
			if entry.row.ClientID == g.clientID &&
				entry.row.UserID == g.userID &&
				entry.row.State == DeviceAuthorizationStateApproved &&
				entry.row.ConsumedAt == nil {
				ts := nowUTC
				entry.row.ConsumedAt = &ts
			}
		}
	case invalidateModeClient:
		for _, entry := range s.byHash {
			if entry.row.ClientID == g.clientID && entry.row.ConsumedAt == nil {
				ts := nowUTC
				entry.row.ConsumedAt = &ts
			}
		}
	case invalidateModeDeleteClient:
		for key, entry := range s.byHash {
			if entry.row.ClientID == g.clientID {
				delete(s.byHash, key)
				if s.byUserCode[entry.row.UserCode] == entry {
					delete(s.byUserCode, entry.row.UserCode)
				}
			}
		}
	}
}

func (s *MemoryDeviceAuthorizationStore) AbortInvalidate(tok DeviceInvalidateToken) {
	if tok.id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.invalidations, tok.id)
}

func (s *MemoryDeviceAuthorizationStore) ConsumeApprovedForUserBeforeGeneration(userID string, userAuthGeneration int64, now time.Time) int64 {
	if userID == "" {
		return 0
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, cr := range s.consumeRes {
		if cr.userID == userID && auth.ShouldEvictForUserGeneration(cr.authGeneration, userAuthGeneration) {
			cr.invalidated = true
		}
	}

	var consumed int64
	for _, entry := range s.byHash {
		if entry.row.UserID == userID &&
			entry.row.State == DeviceAuthorizationStateApproved &&
			entry.row.ConsumedAt == nil &&
			auth.ShouldEvictForUserGeneration(entry.row.AuthGeneration, userAuthGeneration) {
			ts := nowUTC
			entry.row.ConsumedAt = &ts
			consumed++
		}
	}
	return consumed
}

func (s *MemoryDeviceAuthorizationStore) DeleteExpired(now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked(now.UTC())
}

func (s *MemoryDeviceAuthorizationStore) evictExpiredLocked(now time.Time) int64 {
	var deleted int64
	for key, entry := range s.byHash {
		if entry.consumeResID != 0 || entry.approveResID != 0 {
			continue
		}
		if entry.row.ExpiresAt.Before(now) {
			delete(s.byHash, key)
			if s.byUserCode[entry.row.UserCode] == entry {
				delete(s.byUserCode, entry.row.UserCode)
			}
			deleted++
		}
	}
	return deleted
}

func cloneDeviceAuthorization(src *DeviceAuthorization) *DeviceAuthorization {
	if src == nil {
		return nil
	}
	cp := *src
	cp.DeviceCodeHash = bytes.Clone(src.DeviceCodeHash)
	if src.LastPolledAt != nil {
		ts := *src.LastPolledAt
		cp.LastPolledAt = &ts
	}
	if src.ConsumedAt != nil {
		ts := *src.ConsumedAt
		cp.ConsumedAt = &ts
	}
	return &cp
}
