// Package channelmgr manages the frontend side of encrypted channel connections.
// It tracks active channels and routes opaque ciphertext between WebSocket clients
// (Frontend) and Worker bidi streams.
package channelmgr

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
)

// SendFunc is the signature for sending a ChannelMessage to a frontend client.
type SendFunc func(msg *leapmuxv1.ChannelMessage) error

// channel represents an active encrypted channel between a frontend and worker.
//
// AuthInfo.Credential identifies the session or bearer row that authenticated
// the OpenChannel call. CloseByBearer retains the table kind so api_tokens.id
// and delegation_tokens.id do not share a revocation namespace.
//
// UserAuthGeneration is the persisted user credential epoch observed by
// the credential that opened the channel. User-wide revocation closes
// channels with an older generation.
type channel struct {
	opMu        sync.Mutex
	closed      bool
	ChannelID   string
	WorkerID    string
	UserID      string
	AuthInfo    AuthInfo
	ConnID      string // Multiplexed connection that owns this channel (empty until first message).
	expiresAt   auth.CredentialDeadline
	expiryIndex int
	// pendingExpiry is a fresh deadline a rotation/slide recorded while the channel
	// was still being opened -- registered and indexed, but before ScheduleExpiry
	// armed its timer. ScheduleExpiry adopts it verbatim (including a NeverExpires
	// that clears the deadline) instead of the stale connect-time base. An Unset
	// pendingExpiry means no reschedule landed in the open window -- the three
	// states are the CredentialDeadline's own, so there is no separate flag or
	// pointer nil-ness to keep in sync and no zero-vs-unset ambiguity.
	pendingExpiry auth.CredentialDeadline
	onExpire      func(ClosedChannel)
	cancel        context.CancelFunc
}

// userConn represents a single multiplexed WebSocket connection for a user.
type userConn struct {
	sendFn SendFunc
	cancel context.CancelFunc
}

// Manager tracks active encrypted channel connections.
//
// Multiple WebSocket connections can exist per user, registered via BindUser().
// Each channel is associated with a specific connection via UseAuthorizedChannel
// when the relay first receives an authorized message. SendToFrontend
// routes to that specific connection.
type Manager struct {
	mu                sync.RWMutex
	channels          map[string]*channel            // channelID -> channel
	channelsByUser    map[string]map[string]struct{} // userID -> channel IDs
	channelsByWorker  map[string]map[string]struct{} // workerID -> channel IDs
	channelsBySession map[string]map[string]struct{} // sessionID -> channel IDs
	channelsByBearer  map[auth.BearerRef]map[string]struct{}
	userSenders       map[string]map[string]*userConn // userID -> connID -> multiplexed WS connection
	expiries          channelExpiryHeap
	expiryTimer       *time.Timer
	ChunkTracker      *chunkTracker // chunk-aware relay enforcement
}

// New creates a new channel Manager.
func New(opts ...Option) *Manager {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return &Manager{
		channels:          make(map[string]*channel),
		channelsByUser:    make(map[string]map[string]struct{}),
		channelsByWorker:  make(map[string]map[string]struct{}),
		channelsBySession: make(map[string]map[string]struct{}),
		channelsByBearer:  make(map[auth.BearerRef]map[string]struct{}),
		userSenders:       make(map[string]map[string]*userConn),
		ChunkTracker:      newChunkTracker(cfg.maxMessageSize, cfg.maxIncompleteChunked),
	}
}

// config holds optional configuration for the Manager.
type config struct {
	maxMessageSize       int
	maxIncompleteChunked int
}

func defaultConfig() *config {
	return &config{
		maxMessageSize:       channelwire.DefaultMaxMessageSize,
		maxIncompleteChunked: channelwire.DefaultMaxIncompleteChunked,
	}
}

// Option configures a Manager.
type Option func(*config)

// WithMaxMessageSize sets the maximum reassembled message size.
func WithMaxMessageSize(size int) Option {
	return func(c *config) { c.maxMessageSize = size }
}

// WithMaxIncompleteChunked sets the maximum number of in-flight chunked
// sequences per channel.
func WithMaxIncompleteChunked(n int) Option {
	return func(c *config) { c.maxIncompleteChunked = n }
}

type AuthInfo struct {
	Credential          auth.CredentialIdentity
	UserAuthGeneration  int64
	CredentialExpiresAt auth.CredentialDeadline
}

// ChannelInfo is a lock-consistent snapshot of a registered channel's
// routing and authentication basis.
type ChannelInfo struct {
	ChannelID string
	WorkerID  string
	UserID    string
	AuthInfo  AuthInfo
}

// RegisterWithAuthInfo records the full authentication basis for a channel.
// OpenChannel callers must provide UserAuthGeneration so user-wide revocation
// can use the persisted credential epoch.
func (m *Manager) RegisterWithAuthInfo(
	channelID, workerID, userID string,
	authInfo AuthInfo,
	cancel context.CancelFunc,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := &channel{
		ChannelID:   channelID,
		WorkerID:    workerID,
		UserID:      userID,
		AuthInfo:    authInfo,
		expiryIndex: -1,
		cancel:      cancel,
	}
	m.channels[channelID] = ch
	m.indexChannel(ch)
}

// indexChannel adds ch to every credential/routing reverse index. Mirror of
// unindexChannel; keeping the two together stops a future index from being
// added on registration but missed on teardown (a channel leak). Caller holds
// m.mu.
func (m *Manager) indexChannel(ch *channel) {
	addChannelIndex(m.channelsByUser, ch.UserID, ch.ChannelID)
	addChannelIndex(m.channelsByWorker, ch.WorkerID, ch.ChannelID)
	if sessionID := ch.AuthInfo.Credential.SessionID(); sessionID != "" {
		addChannelIndex(m.channelsBySession, sessionID, ch.ChannelID)
	}
	if ref, ok := ch.AuthInfo.Credential.BearerRef(); ok {
		addChannelIndex(m.channelsByBearer, ref, ch.ChannelID)
	}
}

// unindexChannel removes ch from every reverse index indexChannel populated.
// Caller holds m.mu.
func (m *Manager) unindexChannel(ch *channel) {
	removeChannelIndex(m.channelsByUser, ch.UserID, ch.ChannelID)
	removeChannelIndex(m.channelsByWorker, ch.WorkerID, ch.ChannelID)
	if sessionID := ch.AuthInfo.Credential.SessionID(); sessionID != "" {
		removeChannelIndex(m.channelsBySession, sessionID, ch.ChannelID)
	}
	if ref, ok := ch.AuthInfo.Credential.BearerRef(); ok {
		removeChannelIndex(m.channelsByBearer, ref, ch.ChannelID)
	}
}

func addChannelIndex[K comparable](index map[K]map[string]struct{}, key K, channelID string) {
	ids := index[key]
	if ids == nil {
		ids = make(map[string]struct{})
		index[key] = ids
	}
	ids[channelID] = struct{}{}
}

func removeChannelIndex[K comparable](index map[K]map[string]struct{}, key K, channelID string) {
	ids := index[key]
	delete(ids, channelID)
	if len(ids) == 0 {
		delete(index, key)
	}
}

// BindUser registers a multiplexed WebSocket connection for a user.
// Multiple connections can coexist for the same user (e.g. browser + test helper).
// Each connection is identified by connID.
func (m *Manager) BindUser(userID, connID string, sendFn SendFunc, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conns := m.userSenders[userID]
	if conns == nil {
		conns = make(map[string]*userConn)
		m.userSenders[userID] = conns
	}
	conns[connID] = &userConn{sendFn: sendFn, cancel: cancel}
}

// UnbindUser removes a specific multiplexed WebSocket connection for a user.
// Only the connection identified by connID is removed; other connections for the
// same user are unaffected. The connection's cancel function is called.
// Returns true if the user has no remaining connections.
func (m *Manager) UnbindUser(userID, connID string) bool {
	m.mu.Lock()
	uc, noConns := m.unbindLocked(userID, connID)
	m.mu.Unlock()

	if uc != nil && uc.cancel != nil {
		uc.cancel()
	}
	return noConns
}

// unbindLocked removes a user's connection while m.mu is held. Returns the
// removed userConn (so the caller can invoke its cancel outside the lock)
// and whether the user has any remaining connections.
func (m *Manager) unbindLocked(userID, connID string) (*userConn, bool) {
	conns := m.userSenders[userID]
	if conns == nil {
		return nil, true
	}
	uc := conns[connID]
	delete(conns, connID)
	if len(conns) == 0 {
		delete(m.userSenders, userID)
		return uc, true
	}
	return uc, false
}

// UseAuthorizedChannel binds a relay connection and performs one routed
// operation while holding the channel's operation lock. Credential teardown
// waits for the operation to finish before removing the channel and publishing
// close notifications, so no routed message can cross the revocation boundary.
func (m *Manager) UseAuthorizedChannel(
	channelID, connID string,
	authorize func(ChannelInfo) bool,
	operation func(ChannelInfo) error,
) (ChannelInfo, bool, error) {
	return m.useChannel(channelID, &connID, authorize, operation)
}

// UseChannelIf performs one operation on a live channel without changing its
// frontend binding. Channel open uses it to serialize the worker open attempt
// with revocation teardown.
func (m *Manager) UseChannelIf(
	channelID string,
	authorize func(ChannelInfo) bool,
	operation func(ChannelInfo) error,
) (ChannelInfo, bool, error) {
	return m.useChannel(channelID, nil, authorize, operation)
}

// acquireChannelOp looks up a channel and acquires its operation lock (opMu),
// returning (nil, false) when the channel is not registered. The caller MUST
// re-validate liveness under m.mu with channelLiveLocked before acting -- the
// channel can be torn down between the lookup and the opMu acquisition -- and
// MUST release opMu when done. Centralizing this lookup-then-opMu prologue keeps
// the opMu-before-m.mu ordering identical across every routed operation (open,
// relay, send, close), so a new operation cannot acquire the locks out of order.
func (m *Manager) acquireChannelOp(channelID string) (*channel, bool) {
	m.mu.RLock()
	ch := m.channels[channelID]
	m.mu.RUnlock()
	if ch == nil {
		return nil, false
	}
	ch.opMu.Lock()
	return ch, true
}

// channelLiveLocked reports whether ch is still the registered, open channel for
// channelID. Caller holds m.mu (read or write). A routed operation that waited
// on opMu must re-check this because teardown may have removed or replaced the
// channel while it waited. Sharing one predicate stops the closed/identity
// re-check from drifting between the relay, send, open, and close paths.
func (m *Manager) channelLiveLocked(channelID string, ch *channel) bool {
	return !ch.closed && m.channels[channelID] == ch
}

func (m *Manager) useChannel(
	channelID string,
	bindConnID *string,
	authorize func(ChannelInfo) bool,
	operation func(ChannelInfo) error,
) (ChannelInfo, bool, error) {
	ch, ok := m.acquireChannelOp(channelID)
	if !ok {
		return ChannelInfo{}, false, nil
	}
	// Defer the opMu release so a panic in the authorize/operation callback (the
	// worker-open SendAndWait, validateCurrentAuth) cannot leave opMu held
	// forever. A leaked opMu blocks every later routed op AND every teardown
	// (removeChannelIf re-acquires opMu), so a bearer/session/user revocation
	// could no longer close the channel -- defeating the exact boundary this
	// manager exists to enforce.
	defer ch.opMu.Unlock()

	// The liveness re-check, authorize callback, and ConnID bind run under m.mu;
	// scope them to a closure with a deferred Unlock so a panicking authorize
	// releases the manager-wide lock instead of freezing the whole manager.
	info, ok := func() (ChannelInfo, bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.channelLiveLocked(channelID, ch) {
			return ChannelInfo{}, false
		}
		info := channelInfo(ch)
		if authorize != nil && !authorize(info) {
			return ChannelInfo{}, false
		}
		if bindConnID != nil {
			ch.ConnID = *bindConnID
		}
		return info, true
	}()
	if !ok {
		return ChannelInfo{}, false, nil
	}

	var err error
	if operation != nil {
		err = operation(info)
	}
	return info, true, err
}

// ScheduleExpiry closes channelID at expiresAt and passes the removed channel
// to onExpire. The manager uses one timer for the earliest heap entry; normal
// unregister and revocation paths remove their entry and reset that timer.
func (m *Manager) ScheduleExpiry(channelID string, expiresAt auth.CredentialDeadline, onExpire func(ClosedChannel)) bool {
	if onExpire == nil {
		// A scheduled channel is identified downstream by ch.onExpire != nil:
		// rescheduleExpiryLocked routes a mid-open reschedule to pendingExpiry
		// (vs re-timing the heap) precisely when onExpire == nil. Reject a nil
		// callback here so that invariant -- "scheduled implies onExpire != nil" --
		// holds mechanically at the entry point rather than by caller convention: a
		// nil-onExpire schedule would arm a channel that a later reschedule then
		// misreads as "still opening" and silently drops. No production caller
		// passes nil, so this never fires today.
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.channels[channelID]
	if !ok || !m.channelLiveLocked(channelID, ch) {
		// The channel was torn down (revocation, worker disconnect) during the
		// open handshake. Report failure so OpenChannel does not commit a
		// registration for a channel that no longer exists -- including the
		// never-expires case, which previously short-circuited past this liveness
		// check and returned a phantom-open success. Route the check through
		// channelLiveLocked (the same predicate relay/send/open/close use) so a
		// closed-but-still-mapped channel can never be scheduled.
		return false
	}
	// Adopt any deadline a rotation/slide recorded on this channel while it was
	// being opened (before it was scheduled): that reschedule is authoritative, so
	// take it verbatim -- including a NeverExpires that clears the deadline,
	// matching how the already-scheduled path honors a clear -- rather than the
	// stale connect-time base. With no reschedule on file (pendingExpiry Unset),
	// honor the connect-time base the caller passed.
	effective := expiresAt
	if !ch.pendingExpiry.IsUnset() {
		effective = ch.pendingExpiry
		ch.pendingExpiry = auth.UnsetDeadline()
	}
	ch.AuthInfo.CredentialExpiresAt = effective
	ch.onExpire = onExpire
	if ch.expiryIndex >= 0 {
		heap.Remove(&m.expiries, ch.expiryIndex)
	}
	ch.expiresAt = effective
	if _, ok := effective.At(); ok {
		heap.Push(&m.expiries, ch)
	}
	m.resetExpiryTimerLocked()
	return true
}

// RescheduleExpiryBySession moves the scheduled expiry of every channel
// authenticated by sessionID to newExpiry. Used when a sliding cookie session
// extends its lifetime so channels opened earlier are not torn down at the
// stale connect-time deadline. A NeverExpires newExpiry clears the deadline (an
// At extends it); callers pass DeadlineAt/NeverExpires, never the zero-value
// Unset.
func (m *Manager) RescheduleExpiryBySession(sessionID string, newExpiry auth.CredentialDeadline) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rescheduleExpiryLocked(m.channelsBySession[sessionID], newExpiry)
}

// RescheduleExpiryByBearer moves the scheduled expiry of every channel
// authenticated by the given bearer token to newExpiry. Used when a refresh
// rotates a bearer and extends its access lifetime.
func (m *Manager) RescheduleExpiryByBearer(ref auth.BearerRef, newExpiry auth.CredentialDeadline) {
	if !ref.IsValid() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rescheduleExpiryLocked(m.channelsByBearer[ref], newExpiry)
}

// rescheduleExpiryLocked re-times the given channels' heap entries to newExpiry
// (or removes them when newExpiry has no finite instant -- NeverExpires) and
// refreshes the manager timer. A
// channel still being opened (onExpire == nil, not yet in the heap) has only its
// recorded expiry updated, so the pending ScheduleExpiry arms at the new
// deadline rather than the stale connect-time one; it is not heap-scheduled
// here. Caller holds m.mu.
func (m *Manager) rescheduleExpiryLocked(ids map[string]struct{}, newExpiry auth.CredentialDeadline) {
	changed := false
	for id := range ids {
		ch := m.channels[id]
		if ch == nil {
			continue
		}
		if ch.onExpire == nil {
			// Channel still being opened (not yet in the heap): record the deadline
			// so the pending ScheduleExpiry adopts it -- including a NeverExpires
			// that clears the deadline -- instead of the stale connect-time base.
			// The CredentialDeadline distinguishes this recorded reschedule
			// (never/at) from "no reschedule" (Unset) without a pointer.
			// AdoptReschedule keeps a still-later finite pending deadline against an
			// out-of-order earlier extension (a first reschedule off Unset is
			// adopted verbatim), so the arming path never regresses.
			ch.pendingExpiry = ch.pendingExpiry.AdoptReschedule(newExpiry)
			continue
		}
		// Do not let an out-of-order reschedule regress a still-later finite
		// deadline (concurrent same-credential extensions can arrive reversed),
		// matching the cache-side monotonic RecordBearerExpiry.
		next := ch.expiresAt.AdoptReschedule(newExpiry)
		ch.AuthInfo.CredentialExpiresAt = next
		ch.expiresAt = next
		if _, ok := next.At(); !ok {
			// Cleared to NeverExpires: drop the channel from the expiry heap.
			if ch.expiryIndex >= 0 {
				heap.Remove(&m.expiries, ch.expiryIndex)
			}
			changed = true
			continue
		}
		if ch.expiryIndex >= 0 {
			heap.Fix(&m.expiries, ch.expiryIndex)
		} else {
			heap.Push(&m.expiries, ch)
		}
		changed = true
	}
	if changed {
		m.resetExpiryTimerLocked()
	}
}

func (m *Manager) resetExpiryTimerLocked() {
	if len(m.expiries) == 0 {
		if m.expiryTimer != nil {
			m.expiryTimer.Stop()
		}
		return
	}
	delay := expiryDelay(m.expiries[0].expiresAt)
	if m.expiryTimer == nil {
		m.expiryTimer = time.AfterFunc(delay, m.expireDueChannels)
		return
	}
	m.expiryTimer.Reset(delay)
}

// channelExpireConcurrency caps how many due channels the expiry sweep tears
// down at once, so a large simultaneously-expiring cohort cannot spawn an
// unbounded goroutine burst all contending on m.mu. Sized to keep the timely
// teardown of the many from being head-of-line-blocked by a few wedged ops
// without letting the fan-out itself become the bottleneck.
const channelExpireConcurrency = 32

func (m *Manager) expireDueChannels() {
	now := time.Now()

	// Phase 1 (sequential, under m.mu): drain the heap of every currently-due
	// channel ID and reset the timer. Popping the IDs here -- rather than tearing
	// each channel down inline -- keeps the blocking opMu teardown below OFF the
	// heap walk, so a channel whose routed op is wedged cannot head-of-line-block
	// the expiry of the others.
	var dueIDs []string
	m.mu.Lock()
	for len(m.expiries) > 0 {
		top := m.expiries[0]
		at, ok := top.expiresAt.At()
		// Only At deadlines belong in the heap (ScheduleExpiry never pushes a
		// never/unset; rescheduleExpiryLocked heap.Removes on clear). Defend that
		// invariant rather than trust it: a non-At entry has no finite instant to
		// order or compare against, so without this drop a stray one would be
		// re-selected forever (and re-arm the timer at 0), spinning the sweep. Drop
		// the stray entry; the channel keeps living (never expires).
		if !ok {
			heap.Pop(&m.expiries)
			continue
		}
		if now.Before(at) {
			break
		}
		heap.Pop(&m.expiries)
		dueIDs = append(dueIDs, top.ChannelID)
	}
	m.resetExpiryTimerLocked()
	m.mu.Unlock()
	if len(dueIDs) == 0 {
		return
	}

	// Phase 2 (concurrent, bounded): tear each due channel down under its OWN
	// opMu. acquireChannelOp waits for an in-flight routed op off m.mu, so a wedged
	// op blocks only its own goroutine, not the sweep. The predicate re-checks
	// liveness and due-ness under m.mu, so a channel that a concurrent
	// slide/rotation rescheduled (pushed back into the heap) between the Phase 1
	// pop and here is left alone. drainRemoved's close notification is a
	// coder/websocket write (serialized internally) and ChunkTracker is
	// self-locked, and drainRemoved already runs concurrently across the revocation
	// and disconnect paths, so per-channel concurrent teardown adds no new hazard.
	// The bounded fan-out (see fanOutTeardown) caps the goroutine + m.mu-contention
	// burst for a large simultaneously-expiring cohort and recovers each teardown's
	// panic; it waits for the slowest single channel, not their sum.
	fanOutTeardown(dueIDs, func(channelID string) {
		var onExpire func(ClosedChannel)
		removed, ok := m.removeChannelIf(channelID, func(ch *channel) bool {
			// Not At (never/unset) or not yet due -- a slide/rotation rescheduled
			// it out from under the Phase 1 pop; leave it alone.
			if at, ok := ch.expiresAt.At(); !ok || now.Before(at) {
				return false
			}
			onExpire = ch.onExpire
			ch.onExpire = nil
			return true
		})
		if !ok {
			return
		}
		m.drainRemoved([]removedChannel{removed})
		if onExpire != nil {
			onExpire(removed.closed)
		}
	})
}

// fanOutTeardown runs work(id) for every id concurrently, capped at
// channelExpireConcurrency, and blocks until all complete. A large cohort torn
// down at one instant (a session/bearer TTL expiring many channels at once, or a
// mass revocation) would otherwise spawn one goroutine per channel all thundering
// on m.mu; the semaphore bounds that burst while preserving the
// anti-head-of-line-blocking property -- one wedged op holds one slot, so healthy
// teardowns keep flowing until every slot is held at once (then bounded by the
// per-op worker send timeout). Each detached teardown recovers its own panic:
// drainRemoved writes a frontend close frame and expiry runs a caller-supplied
// onExpire, either of which could panic, and this goroutine has no caller to
// propagate to -- an unrecovered panic would crash the whole Hub instead of
// dropping one channel's notification (the same defense the sibling
// service/channel_close_dispatcher.go applies). Shared by expireDueChannels and
// closeChannelIDs so the cap, recovery, and wait accounting cannot drift.
func fanOutTeardown(ids []string, work func(id string)) {
	sem := make(chan struct{}, channelExpireConcurrency)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("recovered from panic in channel teardown", "channel_id", id, "panic", r)
				}
			}()
			work(id)
		}(id)
	}
	wg.Wait()
}

// AuthorizedChannelIDsForUserWorker returns the IDs of userID's channels on
// workerID that satisfy authorize. The user/worker filtering is routing the
// manager owns; the authorize predicate carries the caller's authorization
// policy, so delegation-scope rules live in the service layer beside the other
// channel-auth checks rather than inside this routing index.
func (m *Manager) AuthorizedChannelIDsForUserWorker(userID, workerID string, authorize func(ChannelInfo) bool) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ids []string
	for id := range m.channelsByUser[userID] {
		ch := m.channels[id]
		if ch == nil || ch.WorkerID != workerID {
			continue
		}
		if authorize == nil || authorize(channelInfo(ch)) {
			ids = append(ids, id)
		}
	}
	return ids
}

// UnregisterByWorker removes all channels for a given worker (e.g. on disconnect).
// Close notifications are sent to the owning frontend connection so clients can
// detect dead channels without waiting for RPC timeouts.
func (m *Manager) UnregisterByWorker(workerID string) []string {
	channelIDs := snapshotIndexedChannelIDs(m, m.channelsByWorker, workerID)
	closed := m.closeChannelIDs(channelIDs, nil)
	ids := make([]string, len(closed))
	for i, cc := range closed {
		ids[i] = cc.ChannelID
	}
	return ids
}

// ClosedChannel holds the IDs of a channel that was unregistered, so the
// caller can notify the worker.
type ClosedChannel struct {
	ChannelID string
	WorkerID  string
	UserID    string
}

type removedChannel struct {
	closed ClosedChannel
	sender SendFunc
	cancel context.CancelFunc
}

// removeChannelIf claims one channel without holding the manager lock while it
// waits for an in-flight routed operation. The predicate is evaluated after
// both locks are acquired, making authorization and removal one atomic action.
func (m *Manager) removeChannelIf(channelID string, predicate func(*channel) bool) (removedChannel, bool) {
	ch, ok := m.acquireChannelOp(channelID)
	if !ok {
		return removedChannel{}, false
	}
	// Defer opMu / m.mu so a panic in the caller-supplied predicate cannot leave
	// either lock held forever (see useChannel).
	defer ch.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.channelLiveLocked(channelID, ch) || (predicate != nil && !predicate(ch)) {
		return removedChannel{}, false
	}
	ch.closed = true
	delete(m.channels, channelID)
	m.unindexChannel(ch)
	if ch.expiryIndex >= 0 {
		heap.Remove(&m.expiries, ch.expiryIndex)
	}
	removed := removedChannel{
		closed: ClosedChannel{ChannelID: channelID, WorkerID: ch.WorkerID, UserID: ch.UserID},
		sender: m.getConnSender(ch.UserID, ch.ConnID),
		cancel: ch.cancel,
	}
	return removed, true
}

func (m *Manager) drainRemoved(removed []removedChannel) []ClosedChannel {
	closed := make([]ClosedChannel, len(removed))
	for i, item := range removed {
		closed[i] = item.closed
		sendCloseNotification(item.sender, item.closed.ChannelID)
		m.ChunkTracker.RemoveChannel(item.closed.ChannelID)
		if item.cancel != nil {
			item.cancel()
		}
	}
	return closed
}

// closeMatchingWithLocked backs UnbindUserAndCleanup: it lets the caller run
// extra state mutations (e.g. unbinding a user connection) inside the same
// lock window that selects the sweep. The locked callback runs first and
// returns the candidate channel IDs to consider (scoped through an index so
// we don't walk every channel in the manager) together with the predicate
// that picks among them. removeChannelIf evaluates that predicate again while
// holding m.mu, so predicates that inspect manager state do not act on a
// stale snapshot after waiting for an in-flight channel operation.
//
// `extraCancel` is invoked outside the lock after channel cancels run;
// callers that unbound a connection use it to cancel the connection's
// own ctx.
func (m *Manager) closeMatchingWithLocked(extraCancel func(), build func(m *Manager) (candidateIDs []string, predicate func(*channel) bool)) []ClosedChannel {
	// build + predicate are caller-supplied and run under m.mu; scope them to a
	// closure with a deferred Unlock so a panic in either releases the
	// manager-wide lock instead of freezing the whole manager (see useChannel).
	ids, predicate := func() ([]string, func(*channel) bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		candidateIDs, predicate := build(m)
		var ids []string
		for _, id := range candidateIDs {
			if ch := m.channels[id]; ch != nil && predicate(ch) {
				ids = append(ids, id)
			}
		}
		return ids, predicate
	}()
	closed := m.closeChannelIDs(ids, predicate)
	if extraCancel != nil {
		extraCancel()
	}
	return closed
}

func (m *Manager) closeChannelIDs(ids []string, predicate func(*channel) bool) []ClosedChannel {
	// Tear each channel down under its OWN opMu, concurrently and bounded, so a
	// channel whose routed operation is wedged on an unresponsive worker cannot
	// head-of-line-block the teardown of the others -- the same anti-HOL-blocking
	// property expireDueChannels has. This matters most on the revocation paths
	// (CloseByBearer / CloseBySession / CloseByUserRevocation / CloseByUsers): a
	// revoked credential's remaining channels must stop relaying promptly rather
	// than wait behind one channel mid-open to a stuck worker, which is a security
	// window, not just a latency one. removeChannelIf takes each channel's opMu OFF
	// m.mu, so a wedged op holds only its own slot; the predicate still runs under
	// m.mu (serialized), and drainRemoved already runs concurrently across the
	// expiry/revocation/disconnect paths, so the fan-out adds no new hazard. The cap
	// bounds the goroutine + m.mu-contention burst for a large cohort (a mass
	// revoke); only when all channelExpireConcurrency slots are held by wedged ops
	// at once do healthy teardowns queue behind them, bounded by the per-op worker
	// send timeout (same saturation caveat as expireDueChannels).
	removed := make([]removedChannel, 0, len(ids))
	var removedMu sync.Mutex
	fanOutTeardown(ids, func(id string) {
		item, ok := m.removeChannelIf(id, predicate)
		if !ok {
			return
		}
		removedMu.Lock()
		removed = append(removed, item)
		removedMu.Unlock()
	})
	// Only re-arm the expiry timer when a channel was actually removed: the
	// timer needs re-computing only because removeChannelIf dropped a heap
	// entry, so a no-op teardown (an empty id set from a credential/worker that
	// owns no channels, or an already-gone channel) skips the hot m.mu writer
	// entirely -- these are common on the revocation sweep and worker-disconnect
	// paths.
	if len(removed) > 0 {
		m.mu.Lock()
		m.resetExpiryTimerLocked()
		m.mu.Unlock()
	}
	return m.drainRemoved(removed)
}

func cloneChannelIDs(ids map[string]struct{}) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out
}

func snapshotIndexedChannelIDs[K comparable](m *Manager, index map[K]map[string]struct{}, key K) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneChannelIDs(index[key])
}

// CloseByID removes one exact channel through the shared teardown pipeline.
func (m *Manager) CloseByID(channelID string) []ClosedChannel {
	if channelID == "" {
		return nil
	}
	return m.closeChannelIDs([]string{channelID}, nil)
}

// CloseByIDIf atomically verifies and removes one channel. A failed predicate
// leaves the channel live and returns no close record.
func (m *Manager) CloseByIDIf(channelID string, predicate func(ChannelInfo) bool) []ClosedChannel {
	if channelID == "" {
		return nil
	}
	return m.closeChannelIDs([]string{channelID}, func(ch *channel) bool {
		return predicate == nil || predicate(channelInfo(ch))
	})
}

// CloseByBearer drops every channel that was authenticated by the
// given bearer token id (api_tokens or delegation_tokens primary
// key). Returns the dropped channels so the caller can notify each
// worker via the existing `ChannelClose` payload — channels closed
// here did NOT see a CloseChannel request, so the worker would
// otherwise hold the inner channel state until its own timeout.
//
// An invalid ref (zero kind or empty token id) is rejected (matches no
// rows) so a buggy revoke path can't accidentally tear down every
// cookie-authenticated channel.
func (m *Manager) CloseByBearer(ref auth.BearerRef) []ClosedChannel {
	if !ref.IsValid() {
		return nil
	}
	ids := snapshotIndexedChannelIDs(m, m.channelsByBearer, ref)
	return m.closeChannelIDs(ids, nil)
}

func (m *Manager) CloseBySession(sessionID string) []ClosedChannel {
	if sessionID == "" {
		return nil
	}
	ids := snapshotIndexedChannelIDs(m, m.channelsBySession, sessionID)
	return m.closeChannelIDs(ids, nil)
}

// CloseByUsers drops channels owned by one of userIDs that satisfy authorize
// (nil closes all of their channels). Callers scope the sweep -- e.g. to a
// workspace whose access was removed -- through the predicate, so the manager
// stays free of authorization policy.
func (m *Manager) CloseByUsers(userIDs []string, authorize func(ChannelInfo) bool) []ClosedChannel {
	if len(userIDs) == 0 {
		return nil
	}
	users := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID != "" {
			users[userID] = struct{}{}
		}
	}
	if len(users) == 0 {
		return nil
	}
	channelIDSet := make(map[string]struct{})
	m.mu.RLock()
	for userID := range users {
		for channelID := range m.channelsByUser[userID] {
			channelIDSet[channelID] = struct{}{}
		}
	}
	m.mu.RUnlock()
	var predicate func(*channel) bool
	if authorize != nil {
		predicate = func(ch *channel) bool { return authorize(channelInfo(ch)) }
	}
	return m.closeChannelIDs(cloneChannelIDs(channelIDSet), predicate)
}

// CloseByUserRevocation drops channels for userID whose authentication basis
// predates the given user-wide revocation event. A non-positive
// userAuthGeneration means the committed generation is unknown and every current
// channel is dropped (fail safe) -- the same rule the auth registry's lease /
// session / bearer sweeps apply, shared via auth.ShouldEvictForUserGeneration so
// the four cannot drift.
func (m *Manager) CloseByUserRevocation(userID string, userAuthGeneration int64) []ClosedChannel {
	if userID == "" {
		return nil
	}
	ids := snapshotIndexedChannelIDs(m, m.channelsByUser, userID)
	return m.closeChannelIDs(ids, func(ch *channel) bool {
		return auth.ShouldEvictForUserGeneration(ch.AuthInfo.UserAuthGeneration, userAuthGeneration)
	})
}

// RestampSessionGeneration advances the recorded UserAuthGeneration of every
// channel authenticated by sessionID to newGeneration. A password change keeps
// the acting session alive but bumps the user generation; without re-stamping,
// the follow-up user-wide revocation (which closes channels below the new
// generation) would tear down the surviving session's own channels. Only
// channels at an older generation are advanced, so a concurrently-opened
// newer-generation channel is left untouched.
func (m *Manager) RestampSessionGeneration(sessionID string, newGeneration int64) {
	if sessionID == "" || newGeneration <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.channelsBySession[sessionID] {
		if ch := m.channels[id]; ch != nil && ch.AuthInfo.UserAuthGeneration < newGeneration {
			ch.AuthInfo.UserAuthGeneration = newGeneration
		}
	}
}

// UnbindUserAndCleanup atomically unbinds a relay connection and removes the
// channels affected by that disconnect: channels bound to the connection
// being unbound, plus — only if no other relay connections remain for the
// user — channels for that user that were never bound to any relay.
//
// Unbinding and candidate selection share one manager-lock window. Each
// candidate is revalidated after its in-flight operation finishes, so a
// concurrent new relay binding preserves any channel it successfully claims.
// Returns the closed channels so the caller can notify workers.
func (m *Manager) UnbindUserAndCleanup(userID, connID string) []ClosedChannel {
	// Channel pruning shares closeMatchingWithLocked's drain pipeline (notify
	// senders, drop chunk tracking, cancel ctxs) so we don't grow a
	// parallel teardown path that drifts from the shared teardown core.
	var uc *userConn
	return m.closeMatchingWithLocked(
		func() {
			if uc != nil && uc.cancel != nil {
				uc.cancel()
			}
		},
		func(*Manager) ([]string, func(*channel) bool) {
			uc, _ = m.unbindLocked(userID, connID)
			// Only this user's channels can match, so scan the per-user index
			// rather than every channel in the manager. Unbound channels
			// (ConnID == "") are indexed at registration too, so scoping the
			// scan here does not miss the never-bound-channel sweep.
			candidateIDs := make([]string, 0, len(m.channelsByUser[userID]))
			for id := range m.channelsByUser[userID] {
				candidateIDs = append(candidateIDs, id)
			}
			return candidateIDs, func(ch *channel) bool {
				if ch.UserID != userID {
					return false
				}
				return ch.ConnID == connID || (len(m.userSenders[userID]) == 0 && ch.ConnID == "")
			}
		},
	)
}

// SendToFrontend routes a ChannelMessage from a worker to the frontend client.
//
// Routes to the channel's associated multiplexed connection (ConnID, set by
// UseAuthorizedChannel). ConnID is always set before any worker response arrives
// because the worker only responds to frontend-initiated requests, and
// UseAuthorizedChannel is called when the relay processes the first
// frontend→worker message.
//
// Returns false if the channel is not found or no sender is available.
func (m *Manager) SendToFrontend(msg *leapmuxv1.ChannelMessage) bool {
	return m.SendToFrontendIf(msg, nil)
}

// SendToFrontendIf routes a worker message only when the channel still
// satisfies authorize. Worker relay uses this to prevent one worker from
// injecting ciphertext into another worker's channel by guessing its ID.
func (m *Manager) SendToFrontendIf(msg *leapmuxv1.ChannelMessage, authorize func(ChannelInfo) bool) bool {
	ch, ok := m.acquireChannelOp(msg.GetChannelId())
	if !ok {
		return false
	}
	// Defer opMu so a panic in the authorize callback cannot leak the channel's
	// op lock (see useChannel); scope the RLock section to a closure with a
	// deferred RUnlock for the same reason.
	defer ch.opMu.Unlock()

	connID, sender, ok := func() (string, SendFunc, bool) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		if !m.channelLiveLocked(msg.GetChannelId(), ch) {
			return "", nil, false
		}
		if authorize != nil && !authorize(channelInfo(ch)) {
			return "", nil, false
		}
		connID := ch.ConnID
		return connID, m.getConnSender(ch.UserID, connID), true
	}()
	if !ok {
		return false
	}

	if sender == nil {
		return false
	}

	if err := sender(msg); err != nil {
		slog.Debug("failed to send channel message to frontend connection",
			"channel_id", msg.GetChannelId(),
			"conn_id", connID,
			"error", err,
		)
		return false
	}
	return true
}

// RelayWorkerMessage validates worker ownership and chunk constraints, then
// sends to the bound frontend while holding the channel operation lock. This
// keeps teardown from clearing chunk state between validation and delivery.
func (m *Manager) RelayWorkerMessage(msg *leapmuxv1.ChannelMessage, workerID string) (bool, error) {
	ch, ok := m.acquireChannelOp(msg.GetChannelId())
	if !ok {
		return false, nil
	}
	defer ch.opMu.Unlock()
	m.mu.RLock()
	if !m.channelLiveLocked(msg.GetChannelId(), ch) || ch.WorkerID != workerID {
		m.mu.RUnlock()
		return false, nil
	}
	sender := m.getConnSender(ch.UserID, ch.ConnID)
	m.mu.RUnlock()

	if err := m.ChunkTracker.Track(
		msg.GetChannelId(), "w2fe", msg.GetCorrelationId(), len(msg.GetCiphertext()),
		msg.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
	); err != nil {
		return true, err
	}
	if sender == nil {
		return true, fmt.Errorf("frontend channel sender is unavailable")
	}
	if err := sender(msg); err != nil {
		return true, fmt.Errorf("send to frontend: %w", err)
	}
	return true, nil
}

// GetChannelInfo returns a lock-consistent snapshot of a channel. Callers
// that authorize an operation on a channel should prefer this over separate
// GetUserID/GetWorkerID calls so ownership, worker routing, and auth basis
// cannot be observed from different channel-manager states.
func (m *Manager) GetChannelInfo(channelID string) (ChannelInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[channelID]
	if !ok {
		return ChannelInfo{}, false
	}
	return channelInfo(ch), true
}

func channelInfo(ch *channel) ChannelInfo {
	return ChannelInfo{
		ChannelID: ch.ChannelID,
		WorkerID:  ch.WorkerID,
		UserID:    ch.UserID,
		AuthInfo:  ch.AuthInfo,
	}
}

// getConnSender returns the SendFunc for a specific connection of a user.
// Must be called while holding m.mu (read or write). Returns nil if not found.
func (m *Manager) getConnSender(userID, connID string) SendFunc {
	conns := m.userSenders[userID]
	if conns == nil {
		return nil
	}
	uc := conns[connID]
	if uc == nil {
		return nil
	}
	return uc.sendFn
}

// sendCloseNotification sends a channel-close notification to a single sender.
// Uses the CLOSE flag as the close sentinel.
func sendCloseNotification(sender SendFunc, channelID string) {
	if sender == nil {
		return
	}
	closeMsg := &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       channelID,
		Flags:           leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE,
	}
	if err := sender(closeMsg); err != nil {
		slog.Debug("failed to send channel close notification",
			"channel_id", channelID,
			"error", err,
		)
	}
}

// HubControlChannelID is the reserved channel ID used for Hub-originated
// control frames sent to frontends via the existing /ws/channel WebSocket.
const HubControlChannelID = "_hub"

// SendToUser sends a ChannelMessage to all WebSocket connections of a specific user.
func (m *Manager) SendToUser(userID string, msg *leapmuxv1.ChannelMessage) {
	m.mu.RLock()
	var senders []SendFunc
	if conns := m.userSenders[userID]; conns != nil {
		for _, uc := range conns {
			senders = append(senders, uc.sendFn)
		}
	}
	m.mu.RUnlock()

	for _, sender := range senders {
		if err := sender(msg); err != nil {
			slog.Debug("failed to send control frame to user", "user_id", userID, "error", err)
		}
	}
}

// Exists returns true if the channel exists.
func (m *Manager) Exists(channelID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.channels[channelID]
	return ok
}
