package service

import (
	"bytes"
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
	UserID          userid.UserID
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
	Create(p CreateDeviceAuthorizationParams) error
	Get(deviceCodeHash []byte) (*DeviceAuthorization, bool)
	GetByUserCode(userCode string) (*DeviceAuthorization, bool)
	TouchPoll(deviceCodeHash []byte, now time.Time)
	DenyByUserCode(userCode string) bool
	ApproveByUserCode(p ApproveDeviceAuthorizationParams, now time.Time) (bool, error)
	Consume(deviceCodeHash []byte, now time.Time) bool
	DeleteExpired(now time.Time) int64

	// Two-phase token issuance: reserve before SQL transaction, check
	// IsInvalidated inside the transaction, commit or release after.
	ReserveConsume(deviceCodeHash []byte, now time.Time) (DeviceGrantReservation, bool)
	IsInvalidated(res DeviceGrantReservation) bool
	CommitConsume(res DeviceGrantReservation, now time.Time)
	ReleaseConsume(res DeviceGrantReservation)

	// Two-phase step-up approval: reserve before SQL elevation write, commit
	// or release after.
	ReserveApproveByUserCode(userCode string, userID userid.UserID, authGeneration int64, now time.Time) (DeviceGrantReservation, bool)
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
	ConsumeApprovedForUserBeforeGeneration(userID userid.UserID, userAuthGeneration int64, now time.Time) int64
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
	userID         userid.UserID
	authGeneration int64
	invalidated    bool
}

type approveReservation struct {
	key            string
	clientID       string
	userCode       string
	userID         userid.UserID
	authGeneration int64
	invalidated    bool
}

// withDeviceInvalidation holds tok across fn, committing the invalidation when
// fn returns nil and aborting it on any error or panic.
func withDeviceInvalidation(
	ds DeviceAuthorizationStore,
	tok DeviceInvalidateToken,
	now func() time.Time,
	fn func() error,
) error {
	committed := false
	defer func() {
		if !committed {
			ds.AbortInvalidate(tok)
		}
	}()
	if err := fn(); err != nil {
		return err
	}
	ds.CommitInvalidate(tok, now().UTC())
	committed = true
	return nil
}

// withDeviceConsume reserves deviceCodeHash at reserveNow, runs fn with an
// invalidation check, commits at now().UTC() on nil return, and releases on
// any error or panic.
func withDeviceConsume(
	ds DeviceAuthorizationStore,
	deviceCodeHash []byte,
	reserveNow time.Time,
	now func() time.Time,
	fn func(isInvalidated func() bool) error,
) error {
	res, ok := ds.ReserveConsume(deviceCodeHash, reserveNow)
	if !ok {
		return errAuthorizationGrantUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			ds.ReleaseConsume(res)
		}
	}()
	if err := fn(func() bool { return ds.IsInvalidated(res) }); err != nil {
		return err
	}
	ds.CommitConsume(res, now().UTC())
	committed = true
	return nil
}

// withDeviceStepUpApprove reserves p.UserCode for step-up approval across fn,
// committing the approval when fn returns nil and releasing it on any error
// or panic.
func withDeviceStepUpApprove(
	ds DeviceAuthorizationStore,
	p ApproveDeviceAuthorizationParams,
	now time.Time,
	fn func() error,
) error {
	res, ok := ds.ReserveApproveByUserCode(p.UserCode, p.UserID, p.AuthGeneration, now)
	if !ok {
		return errDeviceGrantNotApprovable
	}
	committed := false
	defer func() {
		if !committed {
			ds.ReleaseApproveByUserCode(res)
		}
	}()
	if err := fn(); err != nil {
		return err
	}
	ds.CommitApproveByUserCode(res, p)
	committed = true
	return nil
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
	userID   userid.UserID
}

func (g *invalidationGuard) matches(clientID string, userID userid.UserID) bool {
	if g.clientID != clientID {
		return false
	}
	if g.mode == invalidateModeUserClient {
		return g.userID.MatchesUser(userID)
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
	minExpiresAt  time.Time
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

func (s *MemoryDeviceAuthorizationStore) hasActiveInvalidationLocked(clientID string, userID userid.UserID) bool {
	for _, g := range s.invalidations {
		if g.matches(clientID, userID) {
			return true
		}
	}
	return false
}

func (s *MemoryDeviceAuthorizationStore) Create(p CreateDeviceAuthorizationParams) error {
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
		if s.minExpiresAt.IsZero() || s.minExpiresAt.Before(now) {
			s.evictExpiredLocked(now)
		}
		if len(s.byHash) >= s.maxEntries {
			return errDeviceAuthCapacity
		}
	}

	exp := p.ExpiresAt.UTC()
	entry := &deviceAuthEntry{
		key: key,
		row: DeviceAuthorization{
			DeviceCodeHash:  bytes.Clone(p.DeviceCodeHash),
			UserCode:        p.UserCode,
			DeviceName:      p.DeviceName,
			ClientID:        p.ClientID,
			RequestedScopes: p.RequestedScopes,
			State:           DeviceAuthorizationStatePending,
			ExpiresAt:       exp,
			CreatedAt:       now,
			ElevateTokenID:  p.ElevateTokenID,
		},
	}
	wasEmpty := len(s.byHash) == 0
	s.byHash[key] = entry
	s.byUserCode[p.UserCode] = entry
	if wasEmpty || (!s.minExpiresAt.IsZero() && exp.Before(s.minExpiresAt)) {
		s.minExpiresAt = exp
	}
	return nil
}

func (s *MemoryDeviceAuthorizationStore) Get(deviceCodeHash []byte) (*DeviceAuthorization, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[string(deviceCodeHash)]
	if !ok {
		return nil, false
	}
	return cloneDeviceAuthorization(&entry.row), true
}

func (s *MemoryDeviceAuthorizationStore) GetByUserCode(userCode string) (*DeviceAuthorization, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byUserCode[userCode]
	if !ok {
		return nil, false
	}
	return cloneDeviceAuthorization(&entry.row), true
}

func (s *MemoryDeviceAuthorizationStore) TouchPoll(deviceCodeHash []byte, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[string(deviceCodeHash)]
	if !ok {
		return
	}
	ts := now.UTC()
	entry.row.LastPolledAt = &ts
}

func (s *MemoryDeviceAuthorizationStore) DenyByUserCode(userCode string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byUserCode[userCode]
	if !ok {
		return false
	}
	if entry.row.State != DeviceAuthorizationStatePending || entry.row.ConsumedAt != nil || entry.approveResID != 0 {
		return false
	}
	entry.row.State = DeviceAuthorizationStateDenied
	return true
}

func (s *MemoryDeviceAuthorizationStore) ApproveByUserCode(p ApproveDeviceAuthorizationParams, now time.Time) (bool, error) {
	if p.UserID.IsZero() {
		return false, fmt.Errorf("approve device authorization requires a user id: %w", store.ErrInvalidArgument)
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byUserCode[p.UserCode]
	if !ok {
		return false, nil
	}
	if entry.row.State != DeviceAuthorizationStatePending ||
		entry.row.ConsumedAt != nil ||
		!entry.row.ExpiresAt.After(nowUTC) ||
		entry.approveResID != 0 ||
		s.hasActiveInvalidationLocked(entry.row.ClientID, p.UserID) {
		return false, nil
	}
	entry.row.State = DeviceAuthorizationStateApproved
	entry.row.UserID = p.UserID
	entry.row.GrantedScopes = p.GrantedScopes
	entry.row.AuthGeneration = p.AuthGeneration
	return true, nil
}

func (s *MemoryDeviceAuthorizationStore) canConsumeLocked(entry *deviceAuthEntry, nowUTC time.Time) bool {
	if entry.row.State != DeviceAuthorizationStateApproved ||
		entry.row.UserID.IsZero() ||
		entry.row.ConsumedAt != nil ||
		!entry.row.ExpiresAt.After(nowUTC) ||
		entry.consumeResID != 0 ||
		entry.approveResID != 0 {
		return false
	}
	return !s.hasActiveInvalidationLocked(entry.row.ClientID, entry.row.UserID)
}

func (s *MemoryDeviceAuthorizationStore) noteEvictableLocked(entry *deviceAuthEntry) {
	if entry.consumeResID != 0 || entry.approveResID != 0 {
		return
	}
	if !s.minExpiresAt.IsZero() && entry.row.ExpiresAt.Before(s.minExpiresAt) {
		s.minExpiresAt = entry.row.ExpiresAt
	}
}

func (s *MemoryDeviceAuthorizationStore) Consume(deviceCodeHash []byte, now time.Time) bool {
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[string(deviceCodeHash)]
	if !ok || !s.canConsumeLocked(entry, nowUTC) {
		return false
	}
	ts := nowUTC
	entry.row.ConsumedAt = &ts
	return true
}

func (s *MemoryDeviceAuthorizationStore) ReserveConsume(deviceCodeHash []byte, now time.Time) (DeviceGrantReservation, bool) {
	nowUTC := now.UTC()
	key := string(deviceCodeHash)

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[key]
	if !ok || !s.canConsumeLocked(entry, nowUTC) {
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

func (s *MemoryDeviceAuthorizationStore) popConsumeReservationLocked(id uint64) (*consumeReservation, *deviceAuthEntry) {
	cr, ok := s.consumeRes[id]
	if !ok {
		return nil, nil
	}
	delete(s.consumeRes, id)
	entry, exists := s.byHash[cr.key]
	if !exists || entry.consumeResID != id {
		return cr, nil
	}
	entry.consumeResID = 0
	s.noteEvictableLocked(entry)
	return cr, entry
}

func (s *MemoryDeviceAuthorizationStore) CommitConsume(res DeviceGrantReservation, now time.Time) {
	if res.id == 0 {
		return
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	_, entry := s.popConsumeReservationLocked(res.id)
	if entry != nil && entry.row.ConsumedAt == nil {
		ts := nowUTC
		entry.row.ConsumedAt = &ts
	}
}

func (s *MemoryDeviceAuthorizationStore) ReleaseConsume(res DeviceGrantReservation) {
	if res.id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cr, entry := s.popConsumeReservationLocked(res.id)
	if entry != nil && cr.invalidated && entry.row.ConsumedAt == nil {
		ts := s.currentTime()
		entry.row.ConsumedAt = &ts
	}
}

func (s *MemoryDeviceAuthorizationStore) ReserveApproveByUserCode(userCode string, userID userid.UserID, authGeneration int64, now time.Time) (DeviceGrantReservation, bool) {
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
		entry.consumeResID != 0 ||
		s.hasActiveInvalidationLocked(entry.row.ClientID, userID) {
		return DeviceGrantReservation{}, false
	}

	resID := s.allocIDLocked()
	entry.approveResID = resID
	s.approveRes[resID] = &approveReservation{
		key:            entry.key,
		clientID:       entry.row.ClientID,
		userCode:       userCode,
		userID:         userID,
		authGeneration: authGeneration,
	}
	return DeviceGrantReservation{id: resID, key: entry.key}, true
}

func (s *MemoryDeviceAuthorizationStore) popApproveReservationLocked(id uint64) (*approveReservation, *deviceAuthEntry) {
	ar, ok := s.approveRes[id]
	if !ok {
		return nil, nil
	}
	delete(s.approveRes, id)
	entry, exists := s.byHash[ar.key]
	if !exists || entry.approveResID != id {
		return ar, nil
	}
	entry.approveResID = 0
	s.noteEvictableLocked(entry)
	return ar, entry
}

func (s *MemoryDeviceAuthorizationStore) CommitApproveByUserCode(res DeviceGrantReservation, p ApproveDeviceAuthorizationParams) {
	if res.id == 0 || p.UserID.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ar, entry := s.popApproveReservationLocked(res.id)
	if entry == nil || entry.row.State != DeviceAuthorizationStatePending || entry.row.ConsumedAt != nil {
		return
	}
	if ar.invalidated {
		ts := s.currentTime()
		entry.row.ConsumedAt = &ts
		return
	}
	entry.row.State = DeviceAuthorizationStateApproved
	entry.row.UserID = p.UserID
	entry.row.GrantedScopes = p.GrantedScopes
	entry.row.AuthGeneration = p.AuthGeneration
}

func (s *MemoryDeviceAuthorizationStore) ReleaseApproveByUserCode(res DeviceGrantReservation) {
	if res.id == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ar, entry := s.popApproveReservationLocked(res.id)
	if entry != nil && ar.invalidated && entry.row.ConsumedAt == nil {
		ts := s.currentTime()
		entry.row.ConsumedAt = &ts
	}
}

func (s *MemoryDeviceAuthorizationStore) beginInvalidate(mode invalidateMode, clientID string, userID userid.UserID) DeviceInvalidateToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.allocIDLocked()
	s.invalidations[id] = &invalidationGuard{
		id:       id,
		mode:     mode,
		clientID: clientID,
		userID:   userID,
	}
	return DeviceInvalidateToken{id: id}
}

func (s *MemoryDeviceAuthorizationStore) BeginInvalidateForUserClient(clientID string, userID userid.UserID) DeviceInvalidateToken {
	return s.beginInvalidate(invalidateModeUserClient, clientID, userID)
}

func (s *MemoryDeviceAuthorizationStore) BeginInvalidateForClient(clientID string) DeviceInvalidateToken {
	return s.beginInvalidate(invalidateModeClient, clientID, userid.UserID{})
}

func (s *MemoryDeviceAuthorizationStore) BeginDeleteForClient(clientID string) DeviceInvalidateToken {
	return s.beginInvalidate(invalidateModeDeleteClient, clientID, userid.UserID{})
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
	for _, ar := range s.approveRes {
		if g.matches(ar.clientID, ar.userID) {
			ar.invalidated = true
		}
	}

	switch g.mode {
	case invalidateModeUserClient:
		for _, entry := range s.byHash {
			if entry.row.ClientID == g.clientID &&
				entry.row.UserID.MatchesUser(g.userID) &&
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
		if len(s.byHash) == 0 {
			s.minExpiresAt = time.Time{}
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

func (s *MemoryDeviceAuthorizationStore) ConsumeApprovedForUserBeforeGeneration(userID userid.UserID, userAuthGeneration int64, now time.Time) int64 {
	if userID.IsZero() {
		return 0
	}
	nowUTC := now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, cr := range s.consumeRes {
		if cr.userID.MatchesUser(userID) && auth.ShouldEvictForUserGeneration(cr.authGeneration, userAuthGeneration) {
			cr.invalidated = true
		}
	}
	for _, ar := range s.approveRes {
		if ar.userID.MatchesUser(userID) && auth.ShouldEvictForUserGeneration(ar.authGeneration, userAuthGeneration) {
			ar.invalidated = true
		}
	}

	var consumed int64
	for _, entry := range s.byHash {
		if entry.row.UserID.MatchesUser(userID) &&
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
	var minRem time.Time
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
			continue
		}
		if minRem.IsZero() || entry.row.ExpiresAt.Before(minRem) {
			minRem = entry.row.ExpiresAt
		}
	}
	s.minExpiresAt = minRem
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
