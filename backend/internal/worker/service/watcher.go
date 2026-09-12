// Package service handles worker requests and distributes events to subscribed channels.
package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registration is one channel's live subscription to one entity.
// The manager stores registrations by value. Each entity keeps its own generation even when several entities share a sender.
type registration struct {
	channelID string
	sender    channel.ResponseWriter
	mode      leapmuxv1.WatchMode

	// gen identifies this registration. Each registration receives a new generation, including registrations for a previously removed channel.
	// broadcast copies the sender and generation before it sends outside the lock.
	// A failed send retires only that generation, so it cannot remove a newer registration.
	gen uint64
}

// watchEntry is one entity a channel wants to watch, with the mode that
// selects how much of its traffic the channel receives.
type watchEntry struct {
	id   string
	mode leapmuxv1.WatchMode
}

// watcherRegistry stores entity ID -> channel ID -> registration for one entity kind.
// The inner map permits only one registration per channel and entity. Replacing it needs no separate deduplication scan.
type watcherRegistry struct {
	mu       sync.RWMutex
	byEntity map[string]map[string]registration
	nextGen  uint64
}

func newWatcherRegistry() *watcherRegistry {
	return &watcherRegistry{byEntity: make(map[string]map[string]registration)}
}

// setWatches replaces this channel's subscriptions with entries.
// Each listed entity receives the sender, requested mode, and a new generation. Omitted entities lose their subscriptions.
// Replacement removes subscriptions for closed tabs because each request supplies the complete current interest.
// Empty entries clear the subscriptions but leave the stream open. Cancellation or channel closure ends the stream.
func (r *watcherRegistry) setWatches(channelID string, entries []watchEntry, sender channel.ResponseWriter) {
	// For repeated entity IDs, keep the last requested mode.
	keep := make(map[string]leapmuxv1.WatchMode, len(entries))
	for _, e := range entries {
		keep[e.id] = normalizeMode(e.mode)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Go permits deletion from a map during iteration.
	for entityID, byChannel := range r.byEntity {
		if _, wanted := keep[entityID]; wanted {
			continue
		}
		delete(byChannel, channelID)
		if len(byChannel) == 0 {
			delete(r.byEntity, entityID)
		}
	}
	for entityID, mode := range keep {
		byChannel := r.byEntity[entityID]
		if byChannel == nil {
			byChannel = make(map[string]registration, 1)
			r.byEntity[entityID] = byChannel
		}
		r.nextGen++
		byChannel[channelID] = registration{
			channelID: channelID,
			sender:    sender,
			mode:      mode,
			gen:       r.nextGen,
		}
	}
}

// unwatchAll drops every subscription held by channelID.
func (r *watcherRegistry) unwatchAll(channelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for entityID, byChannel := range r.byEntity {
		delete(byChannel, channelID)
		if len(byChannel) == 0 {
			delete(r.byEntity, entityID)
		}
	}
}

// hasFullWatcher reports whether a FULL subscription exists for this entity.
// The broadcast path skips content serialization when no subscription can receive it.
func (r *watcherRegistry) hasFullWatcher(entityID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byChannel := r.byEntity[entityID]
	for _, reg := range byChannel {
		if modeIsFull(reg.mode) {
			return true
		}
	}
	return false
}

// modesForChannel returns entity ID -> mode for this channel's registrations.
// watchSession uses the registry to detect promotions without a separate copy of the modes.
func (r *watcherRegistry) modesForChannel(channelID string) map[string]leapmuxv1.WatchMode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]leapmuxv1.WatchMode)
	for entityID, byChannel := range r.byEntity {
		if reg, ok := byChannel[channelID]; ok {
			out[entityID] = reg.mode
		}
	}
	return out
}

// snapshot copies out entityID's registrations under the read lock.
func (r *watcherRegistry) snapshot(entityID string) []registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byChannel := r.byEntity[entityID]
	if len(byChannel) == 0 {
		return nil
	}
	out := make([]registration, 0, len(byChannel))
	for _, reg := range byChannel {
		out = append(out, reg)
	}
	return out
}

// retire drops the registrations whose sends failed.
func (r *watcherRegistry) retire(entityID string, failed []registration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byChannel := r.byEntity[entityID]
	if byChannel == nil {
		return
	}
	dropped := 0
	for _, f := range failed {
		if cur, ok := byChannel[f.channelID]; ok && cur.gen == f.gen {
			delete(byChannel, f.channelID)
			dropped++
		}
	}
	if dropped > 0 && len(byChannel) == 0 {
		delete(r.byEntity, entityID)
	}
}

// errEventNotMarshalable marks an envelope the worker could not encode.
var errEventNotMarshalable = errors.New("watch event could not be marshalled")

// sendFailureLevel reports disconnects at Debug and other send failures at Warn.
// The channel package supplies the same classification to the relay and hub client.
// transportDead cannot select the log level because it also covers failures on a live connection, which require a warning.
func sendFailureLevel(err error) slog.Level {
	if channel.IsTransportTeardown(err) {
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// transportDead reports whether a send failure requires removal of the subscription.
// A rejected message or serialization failure affects one event. Later events can still reach the client.
// Other failures indicate a lost transport. ErrTransportGone is explicit because it is the usual disconnect error.
func transportDead(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, channel.ErrMessageRejected):
		return false
	case errors.Is(err, errEventNotMarshalable):
		return false
	case errors.Is(err, channel.ErrTransportGone):
		return true
	default:
		return true
	}
}

// eventClass separates content for visible tabs from notifications for every subscribed tab.
// A NOTIFY subscription receives notifications only.
type eventClass uint8

const (
	classContent eventClass = iota
	classNotify
)

func agentEventClass(e *leapmuxv1.AgentEvent) eventClass {
	switch e.GetEvent().(type) {
	case *leapmuxv1.AgentEvent_StatusChange,
		*leapmuxv1.AgentEvent_ControlRequest,
		*leapmuxv1.AgentEvent_ControlResponseChanged,
		*leapmuxv1.AgentEvent_ControlCancel,
		*leapmuxv1.AgentEvent_TurnEnd,
		*leapmuxv1.AgentEvent_TodosChanged,
		*leapmuxv1.AgentEvent_BackgroundTasksChanged,
		*leapmuxv1.AgentEvent_ActivityChanged:
		return classNotify
	// Queue snapshots contain text previews for the composer. A NOTIFY subscription does not display them.
	// A promotion to FULL sends a fresh snapshot when the tab becomes visible.
	case *leapmuxv1.AgentEvent_InputQueueChanged,
		*leapmuxv1.AgentEvent_AgentMessage,
		*leapmuxv1.AgentEvent_CatchUpStart,
		*leapmuxv1.AgentEvent_CatchUpComplete:
		return classContent
	default:
		// Restrict unclassified events to visible tabs.
		return classContent
	}
}

func terminalEventClass(e *leapmuxv1.TerminalEvent) eventClass {
	switch e.GetEvent().(type) {
	case *leapmuxv1.TerminalEvent_StatusChange,
		*leapmuxv1.TerminalEvent_Closed,
		*leapmuxv1.TerminalEvent_Bell,
		*leapmuxv1.TerminalEvent_Notification,
		*leapmuxv1.TerminalEvent_TitleChanged,
		*leapmuxv1.TerminalEvent_Progress:
		return classNotify
	case *leapmuxv1.TerminalEvent_Data:
		return classContent
	default:
		return classContent
	}
}

func modeIsFull(mode leapmuxv1.WatchMode) bool {
	return mode == leapmuxv1.WatchMode_WATCH_MODE_FULL
}

// broadcast sends resp to each subscription whose mode permits this event class.
// Content events skip the snapshot and serialization when no FULL subscription exists.
// Callers use this shared mode check. Serialization runs once, when the first eligible subscription needs the payload.
func (r *watcherRegistry) broadcast(entityID string, resp *leapmuxv1.WatchEventsResponse, class eventClass) {
	if class == classContent && !r.hasFullWatcher(entityID) {
		return
	}
	watchers := r.snapshot(entityID)
	if len(watchers) == 0 {
		return
	}

	var (
		payload []byte
		err     error
		dead    []registration
	)
	for _, w := range watchers {
		if class == classContent && !modeIsFull(w.mode) {
			continue
		}
		if payload == nil {
			payload, err = marshalWatchEvent(resp, entityID)
			if err != nil {
				return
			}
		}
		sendErr := w.sender.SendStream(&leapmuxv1.InnerStreamMessage{
			Payload: payload,
		})
		if sendErr == nil {
			continue
		}
		if !transportDead(sendErr) {
			slog.Warn("broadcast: dropping one event; keeping watcher",
				"entity_id", entityID, "channel_id", w.channelID, "error", sendErr)
			continue
		}
		// SendStream reports the failure also. Keep expected disconnects at Debug to prevent repeated warnings for every open subscription.
		// Failures on live connections remain warnings even though both cases retire the subscription.
		slog.Log(context.Background(), sendFailureLevel(sendErr), "broadcast: SendStream failed",
			"entity_id", entityID, "channel_id", w.channelID, "error", sendErr)
		dead = append(dead, w)
	}

	if len(dead) > 0 {
		r.retire(entityID, dead)
	}
}

// WatcherManager manages subscriptions for agent and terminal events.
type WatcherManager struct {
	agents    *watcherRegistry
	terminals *watcherRegistry

	// ownerMu serializes ownership changes with both subscription tables.
	// Always acquire it before a registry lock. A replaced session cannot change its successor's subscriptions.
	ownerMu       sync.Mutex
	channelOwners map[string]uint64 // channelID -> sessionID
	nextSession   uint64
}

// NewWatcherManager creates a new WatcherManager.
func NewWatcherManager() *WatcherManager {
	return &WatcherManager{
		agents:        newWatcherRegistry(),
		terminals:     newWatcherRegistry(),
		channelOwners: make(map[string]uint64),
	}
}

// BeginSession claims the channel and clears its previous subscriptions atomically.
// Clearing the previous FULL modes makes the new session replay its subscribed entities.
func (m *WatcherManager) BeginSession(channelID string) uint64 {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	m.nextSession++
	id := m.nextSession
	m.channelOwners[channelID] = id
	m.agents.unwatchAll(channelID)
	m.terminals.unwatchAll(channelID)
	return id
}

func (m *WatcherManager) isOwner(channelID string, sessionID uint64) bool {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	return m.ownsSessionLocked(channelID, sessionID)
}

// The caller holds ownerMu. Zero identifies an uninitialized session.
func (m *WatcherManager) ownsSessionLocked(channelID string, sessionID uint64) bool {
	return sessionID != 0 && m.channelOwners[channelID] == sessionID
}

// SetAgentWatchesForSession replaces agent subscriptions only while this session owns the channel.
func (m *WatcherManager) SetAgentWatchesForSession(channelID string, sessionID uint64, entries []watchEntry, sender channel.ResponseWriter) {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	if !m.ownsSessionLocked(channelID, sessionID) {
		return
	}
	m.agents.setWatches(channelID, entries, sender)
}

// SetTerminalWatchesForSession replaces terminal subscriptions only while this session owns the channel.
func (m *WatcherManager) SetTerminalWatchesForSession(channelID string, sessionID uint64, entries []watchEntry, sender channel.ResponseWriter) {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	if !m.ownsSessionLocked(channelID, sessionID) {
		return
	}
	m.terminals.setWatches(channelID, entries, sender)
}

// AgentModesForChannel returns the current agent watch modes for channelID.
func (m *WatcherManager) AgentModesForChannel(channelID string) map[string]leapmuxv1.WatchMode {
	return m.agents.modesForChannel(channelID)
}

// TerminalModesForChannel returns the current terminal watch modes for channelID.
func (m *WatcherManager) TerminalModesForChannel(channelID string) map[string]leapmuxv1.WatchMode {
	return m.terminals.modesForChannel(channelID)
}

// UnwatchAll clears channel ownership and every subscription when the channel closes or releases its local stream.
func (m *WatcherManager) UnwatchAll(channelID string) {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	delete(m.channelOwners, channelID)
	m.agents.unwatchAll(channelID)
	m.terminals.unwatchAll(channelID)
}

// UnwatchSession clears ownership and subscriptions only while this session owns the channel.
// A delayed cancellation or exit cannot remove its successor's subscriptions.
func (m *WatcherManager) UnwatchSession(channelID string, sessionID uint64) {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	if !m.ownsSessionLocked(channelID, sessionID) {
		return
	}
	delete(m.channelOwners, channelID)
	m.agents.unwatchAll(channelID)
	m.terminals.unwatchAll(channelID)
}

// BroadcastAgentEvent sends an AgentEvent to all watchers of the given agent.
func (m *WatcherManager) BroadcastAgentEvent(agentID string, event *leapmuxv1.AgentEvent) {
	m.agents.broadcast(agentID, &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
			AgentEvent: event,
		},
	}, agentEventClass(event))
}

// BroadcastTerminalEvent sends a TerminalEvent to all watchers of the given terminal.
func (m *WatcherManager) BroadcastTerminalEvent(terminalID string, event *leapmuxv1.TerminalEvent) {
	m.terminals.broadcast(terminalID, &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
			TerminalEvent: event,
		},
	}, terminalEventClass(event))
}
