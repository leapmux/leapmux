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
type channel struct {
	ChannelID string
	WorkerID  string
	UserID    string
	ConnID    string // Multiplexed connection that owns this channel (empty until first message).
	cancel    context.CancelFunc
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
// a specific multiplexed connection.
func (m *Manager) Register(channelID, workerID, userID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[channelID] = &channel{
		ChannelID: channelID,
		WorkerID:  workerID,
		UserID:    userID,
		cancel:    cancel,
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
	type closeNotif struct {
		sender    SendFunc
		channelID string
	}

	m.mu.Lock()
	var removed []string
	var cancels []context.CancelFunc
	var notifs []closeNotif
	for id, ch := range m.channels {
		if ch.WorkerID == workerID {
			removed = append(removed, id)
			if ch.cancel != nil {
				cancels = append(cancels, ch.cancel)
			}
			if sender := m.getConnSender(ch.UserID, ch.ConnID); sender != nil {
				notifs = append(notifs, closeNotif{sender: sender, channelID: id})
			}
			delete(m.channels, id)
		}
	}
	m.mu.Unlock()

	// Send close notifications outside the lock.
	for _, n := range notifs {
		sendCloseNotification(n.sender, n.channelID)
	}

	for _, id := range removed {
		m.ChunkTracker.RemoveChannel(id)
	}

	for _, cancel := range cancels {
		cancel()
	}
	return removed
}

// ClosedChannel holds the IDs of a channel that was unregistered, so the
// caller can notify the worker.
type ClosedChannel struct {
	ChannelID string
	WorkerID  string
}

// UnregisterByConn removes all channels bound to a specific relay connection
// (e.g. when the WebSocket relay disconnects). Channels whose ConnID matches
// connID are removed and returned so the caller can notify workers.
func (m *Manager) UnregisterByConn(connID string) []ClosedChannel {
	return m.unregisterMatching(func(ch *channel) bool {
		return ch.ConnID == connID
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
	m.mu.Lock()
	uc, noConns := m.unbindLocked(userID, connID)

	var removed []ClosedChannel
	var cancels []context.CancelFunc
	for id, ch := range m.channels {
		if ch.UserID != userID {
			continue
		}
		if ch.ConnID == connID || (noConns && ch.ConnID == "") {
			removed = append(removed, ClosedChannel{ChannelID: id, WorkerID: ch.WorkerID})
			if ch.cancel != nil {
				cancels = append(cancels, ch.cancel)
			}
			delete(m.channels, id)
		}
	}
	m.mu.Unlock()

	if uc != nil && uc.cancel != nil {
		uc.cancel()
	}
	for _, cc := range removed {
		m.ChunkTracker.RemoveChannel(cc.ChannelID)
	}
	for _, cancel := range cancels {
		cancel()
	}
	return removed
}

// unregisterMatching removes all channels matching the predicate.
func (m *Manager) unregisterMatching(predicate func(*channel) bool) []ClosedChannel {
	m.mu.Lock()
	var removed []ClosedChannel
	var cancels []context.CancelFunc
	for id, ch := range m.channels {
		if predicate(ch) {
			removed = append(removed, ClosedChannel{ChannelID: id, WorkerID: ch.WorkerID})
			if ch.cancel != nil {
				cancels = append(cancels, ch.cancel)
			}
			delete(m.channels, id)
		}
	}
	m.mu.Unlock()

	for _, cc := range removed {
		m.ChunkTracker.RemoveChannel(cc.ChannelID)
	}

	for _, cancel := range cancels {
		cancel()
	}
	return removed
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
