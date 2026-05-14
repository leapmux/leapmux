// Package channelmgr manages the frontend side of encrypted channel connections.
// It tracks active channels and routes opaque ciphertext between WebSocket clients
// (Frontend) and Worker bidi streams.
package channelmgr

import (
	"context"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// SendFunc is the signature for sending a ChannelMessage to a frontend client.
type SendFunc func(msg *leapmuxv1.ChannelMessage) error

// channel represents an active encrypted channel between a frontend and worker.
//
// BearerTokenID is the api_tokens / delegation_tokens id that
// authenticated the OpenChannel call when the caller used a bearer
// token; empty for cookie-authenticated channels. CloseByBearer uses
// this to drop every channel an `lmx_…` token authorized when that
// token is revoked.
type channel struct {
	ChannelID     string
	WorkerID      string
	UserID        string
	BearerTokenID string
	ConnID        string // Multiplexed connection that owns this channel (empty until first message).
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
// Each channel is associated with a specific connection via SetChannelConn()
// when the relay first receives a message for that channel. SendToFrontend
// routes to that specific connection.
type Manager struct {
	mu           sync.RWMutex
	channels     map[string]*channel             // channelID -> channel
	userSenders  map[string]map[string]*userConn // userID -> connID -> multiplexed WS connection
	ChunkTracker *chunkTracker                   // chunk-aware relay enforcement
}

// New creates a new channel Manager.
func New(opts ...Option) *Manager {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return &Manager{
		channels:     make(map[string]*channel),
		userSenders:  make(map[string]map[string]*userConn),
		ChunkTracker: newChunkTracker(cfg.maxMessageSize, cfg.maxIncompleteChunked),
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

// Register adds a new channel to the manager. The channel starts without an
// associated connection. Call SetChannelConn() to associate the channel with
// a specific multiplexed connection. For channels authenticated by a bearer
// token (api_tokens or delegation_tokens), prefer RegisterWithBearer so
// CloseByBearer can match later.
func (m *Manager) Register(channelID, workerID, userID string, cancel context.CancelFunc) {
	m.RegisterWithBearer(channelID, workerID, userID, "", cancel)
}

// RegisterWithBearer adds a new channel and records the bearer token
// id that authenticated the OpenChannel call. Pass an empty
// bearerTokenID for cookie-authenticated channels (equivalent to
// Register).
func (m *Manager) RegisterWithBearer(channelID, workerID, userID, bearerTokenID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[channelID] = &channel{
		ChannelID:     channelID,
		WorkerID:      workerID,
		UserID:        userID,
		BearerTokenID: bearerTokenID,
		cancel:        cancel,
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

// SetChannelConn associates a channel with a specific multiplexed connection.
// Called by the WS relay when it first receives a message for a channel,
// establishing which connection owns it. Returns false if the channel doesn't exist.
func (m *Manager) SetChannelConn(channelID, connID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.channels[channelID]
	if !ok {
		return false
	}
	ch.ConnID = connID
	return true
}

// GetChannelIDsForUser returns all channel IDs for a given user.
func (m *Manager) GetChannelIDsForUser(userID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ids []string
	for id, ch := range m.channels {
		if ch.UserID == userID {
			ids = append(ids, id)
		}
	}
	return ids
}

// Unregister removes a channel. It cancels the channel's context, sends a close
// notification to the owning frontend connection, and cleans up.
func (m *Manager) Unregister(channelID string) {
	m.mu.Lock()
	ch, ok := m.channels[channelID]
	var closeSender SendFunc
	if ok {
		delete(m.channels, channelID)
		closeSender = m.getConnSender(ch.UserID, ch.ConnID)
	}
	m.mu.Unlock()

	sendCloseNotification(closeSender, channelID)

	if ok {
		m.ChunkTracker.RemoveChannel(channelID)
		if ch.cancel != nil {
			ch.cancel()
		}
	}
}

// UnregisterByWorker removes all channels for a given worker (e.g. on disconnect).
// Close notifications are sent to the owning frontend connection so clients can
// detect dead channels without waiting for RPC timeouts.
func (m *Manager) UnregisterByWorker(workerID string) []string {
	closed := m.closeMatching(func(ch *channel) bool { return ch.WorkerID == workerID })
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

// closeMatching is the shared core of CloseByBearer / CloseByUser /
// other selector-based closes. The function takes m.mu internally; the
// matched channels are removed from the map and their cancel funcs are
// drained outside the lock so callers don't sit on m.mu through context
// cancellation.
func (m *Manager) closeMatching(predicate func(*channel) bool) []ClosedChannel {
	return m.closeMatchingWithLocked(nil, func(*Manager) func(*channel) bool { return predicate })
}

// closeMatchingWithLocked is the generalised closeMatching: it lets a
// caller run extra state mutations (e.g. unbinding a user connection)
// inside the same lock window that picks the predicate. The locked
// callback runs first; its return value becomes the predicate for the
// channel sweep — this lets the predicate close over freshly mutated
// state (e.g. "noConns" from unbindLocked) without racing a concurrent
// BindUser/Register.
//
// `extraCancel` is invoked outside the lock after channel cancels run;
// callers that unbound a connection use it to cancel the connection's
// own ctx.
func (m *Manager) closeMatchingWithLocked(extraCancel func(), build func(m *Manager) func(*channel) bool) []ClosedChannel {
	type closeNotif struct {
		sender    SendFunc
		channelID string
	}

	m.mu.Lock()
	predicate := build(m)
	var removed []ClosedChannel
	var cancels []context.CancelFunc
	var notifs []closeNotif
	for id, ch := range m.channels {
		if !predicate(ch) {
			continue
		}
		removed = append(removed, ClosedChannel{ChannelID: id, WorkerID: ch.WorkerID, UserID: ch.UserID})
		if ch.cancel != nil {
			cancels = append(cancels, ch.cancel)
		}
		if sender := m.getConnSender(ch.UserID, ch.ConnID); sender != nil {
			notifs = append(notifs, closeNotif{sender: sender, channelID: id})
		}
		delete(m.channels, id)
	}
	m.mu.Unlock()

	if extraCancel != nil {
		extraCancel()
	}
	for _, n := range notifs {
		sendCloseNotification(n.sender, n.channelID)
	}
	for _, cc := range removed {
		m.ChunkTracker.RemoveChannel(cc.ChannelID)
	}
	for _, cancel := range cancels {
		cancel()
	}
	return removed
}

// CloseByBearer drops every channel that was authenticated by the
// given bearer token id (api_tokens or delegation_tokens primary
// key). Returns the dropped channels so the caller can notify each
// worker via the existing `ChannelClose` payload — channels closed
// here did NOT see a CloseChannel request, so the worker would
// otherwise hold the inner channel state until its own timeout.
//
// Empty tokenID is rejected (matches no rows) so a buggy revoke
// path can't accidentally tear down every cookie-authenticated
// channel.
func (m *Manager) CloseByBearer(tokenID string) []ClosedChannel {
	if tokenID == "" {
		return nil
	}
	return m.closeMatching(func(ch *channel) bool {
		return ch.BearerTokenID == tokenID
	})
}

// CloseByUser drops every channel for the given user. Used by
// user-revocation paths (password change, account deletion, admin
// force-logout-all) so spawned-agent channels die in lock-step with
// the row-level revocation. Empty userID is rejected for the same
// reason as CloseByBearer.
func (m *Manager) CloseByUser(userID string) []ClosedChannel {
	if userID == "" {
		return nil
	}
	return m.closeMatching(func(ch *channel) bool {
		return ch.UserID == userID
	})
}

// UnbindUserAndCleanup atomically unbinds a relay connection and removes the
// channels affected by that disconnect: channels bound to the connection
// being unbound, plus — only if no other relay connections remain for the
// user — channels for that user that were never bound to any relay.
//
// All three operations run under a single lock so a concurrent BindUser /
// Register from a new relay session cannot be wiped by the unbound-channel
// sweep. Returns the closed channels so the caller can notify workers.
func (m *Manager) UnbindUserAndCleanup(userID, connID string) []ClosedChannel {
	// Channel pruning shares closeMatching's drain pipeline (notify
	// senders, drop chunk tracking, cancel ctxs) so we don't grow a
	// parallel teardown path that drifts from closeMatching.
	var uc *userConn
	return m.closeMatchingWithLocked(
		func() {
			if uc != nil && uc.cancel != nil {
				uc.cancel()
			}
		},
		func(*Manager) func(*channel) bool {
			noConns := false
			uc, noConns = m.unbindLocked(userID, connID)
			return func(ch *channel) bool {
				if ch.UserID != userID {
					return false
				}
				return ch.ConnID == connID || (noConns && ch.ConnID == "")
			}
		},
	)
}

// SendToFrontend routes a ChannelMessage from a worker to the frontend client.
//
// Routes to the channel's associated multiplexed connection (ConnID, set by
// SetChannelConn). ConnID is always set before any worker response arrives
// because the worker only responds to frontend-initiated requests, and
// SetChannelConn is called when the relay processes the first frontend→worker
// message.
//
// Returns false if the channel is not found or no sender is available.
func (m *Manager) SendToFrontend(msg *leapmuxv1.ChannelMessage) bool {
	m.mu.RLock()
	ch, ok := m.channels[msg.GetChannelId()]
	if !ok {
		m.mu.RUnlock()
		return false
	}

	sender := m.getConnSender(ch.UserID, ch.ConnID)
	m.mu.RUnlock()

	if sender == nil {
		return false
	}

	if err := sender(msg); err != nil {
		slog.Debug("failed to send channel message to frontend connection",
			"channel_id", msg.GetChannelId(),
			"conn_id", ch.ConnID,
			"error", err,
		)
		return false
	}
	return true
}

// GetWorkerID returns the worker ID for a given channel, or empty string if not found.
func (m *Manager) GetWorkerID(channelID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[channelID]
	if !ok {
		return ""
	}
	return ch.WorkerID
}

// GetUserID returns the user ID for a given channel, or empty string if not found.
func (m *Manager) GetUserID(channelID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[channelID]
	if !ok {
		return ""
	}
	return ch.UserID
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
