// Package service watcher provides a fan-out event manager for broadcasting
// WatchEventsResponse messages to subscribed E2EE channel clients.
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
//
// The manager mints every registration, stores it BY VALUE, and never
// hands out a pointer to it. That is what keeps the generation below
// meaningful: a subscriber cannot alias a registration into several
// entities, so registering entity B can no longer overwrite the
// generation entity A was registered with.
type registration struct {
	channelID string
	sender    channel.ResponseWriter
	mode      leapmuxv1.WatchMode

	// gen identifies this registration, minted by the owning registry on
	// every watch call. broadcast snapshots it alongside the sender and
	// hands it back to retire, so a send failure retires only the
	// registration that actually failed -- never a fresher sender
	// installed while the (necessarily unlocked) send loop was in flight.
	//
	// Generations are minted per registry and never reused, so a channel
	// that is dropped and then registers again gets a fresh number, which
	// keeps a still-in-flight broadcast's stale snapshot from matching --
	// and therefore retiring -- the new registration.
	gen uint64
}

// watchEntry is one entity a channel wants to watch, with the mode that
// selects how much of its traffic the channel receives.
type watchEntry struct {
	id   string
	mode leapmuxv1.WatchMode
}

// watcherRegistry is one entity kind's subscription table:
// entity ID -> channel ID -> registration.
//
// The inner map makes "one registration per channel per entity"
// structural rather than a rule the watch path has to re-enforce with a
// linear scan: re-subscribing is a single assignment that cannot half
// apply, and the broadcast path needs no dedup pass before sending.
type watcherRegistry struct {
	mu       sync.RWMutex
	byEntity map[string]map[string]registration
	nextGen  uint64
}

func newWatcherRegistry() *watcherRegistry {
	return &watcherRegistry{byEntity: make(map[string]map[string]registration)}
}

// setWatches makes channelID's subscriptions in this registry exactly
// entries: each listed entity is (re)registered against sender with a
// fresh generation and the requested mode, and every entity this channel
// was watching that the new set omits is dropped.
//
// Replace rather than add, because a WatchEvents request is a statement
// of the client's whole current interest, not an increment. Adding only
// leaked: a client that closed a tab kept the registration for it.
//
// An empty entries list clears every subscription this channel held and
// leaves the stream open -- the cancel frame (or channel close) is what
// retires the stream itself.
func (r *watcherRegistry) setWatches(channelID string, entries []watchEntry, sender channel.ResponseWriter) {
	// Also dedups a request that names the same entity twice (last mode wins).
	keep := make(map[string]leapmuxv1.WatchMode, len(entries))
	for _, e := range entries {
		keep[e.id] = normalizeMode(e.mode)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Deleting from a map while ranging it is defined behaviour in Go.
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

// hasFullWatcher reports whether any channel is registered for entityID in
// FULL mode — used to skip constructing content payloads nobody can receive.
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

// modesForChannel returns entityID -> mode for every registration held by
// channelID. Used by watchSession to detect promotions without a parallel
// session-local mode map.
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

// sendFailureLevel picks the level for a failed send to a channel: Debug when
// the connection was on its way out, Warn for a genuine fault. Deferring to the
// channel package keeps ONE answer to "was this just a disconnect?" across every
// site that reports one -- here, the tunnel relay loops, and the hub client.
//
// Not derivable from transportDead: that answers "should this subscriber be
// retired?" and is true for a transport error on a LIVE connection too, which is
// a real fault and must stay a warning.
func sendFailureLevel(err error) slog.Level {
	if channel.IsTransportTeardown(err) {
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// transportDead classifies a stream-send error: only a genuinely dead transport
// retires a subscription.
//
// A per-message rejection (channel.ErrMessageRejected) is NOT transport death —
// the channel is healthy and the next, smaller event may fit, so treating it as
// fatal would abandon a catch-up replay over one oversized message. Likewise a
// marshal failure (errEventNotMarshalable) says nothing about the transport:
// it is one un-encodable envelope, and retiring over it would lose the message
// page, status, and control requests behind it. Everything else — a closed
// channel, a torn-down session, a write to a gone peer — defaults to dead.
//
// channel.ErrTransportGone is named rather than left to the default arm: it is
// the single most common way a live subscription ends, and stating it here
// keeps the answer a property of this function instead of an accident of what
// the fallthrough happens to return.
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

// eventClass says whether an event is something only a rendered tab can use
// (content) or something every subscribed tab must hear (notify). A NOTIFY
// registration receives only the latter.
type eventClass uint8

const (
	classContent eventClass = iota
	classNotify
)

func agentEventClass(e *leapmuxv1.AgentEvent) eventClass {
	switch e.GetEvent().(type) {
	case *leapmuxv1.AgentEvent_StatusChange,
		*leapmuxv1.AgentEvent_ControlRequest,
		*leapmuxv1.AgentEvent_ControlCancel,
		*leapmuxv1.AgentEvent_TurnEnd,
		*leapmuxv1.AgentEvent_MessageDeleted,
		*leapmuxv1.AgentEvent_TodosChanged,
		*leapmuxv1.AgentEvent_BackgroundTasksChanged:
		return classNotify
	case *leapmuxv1.AgentEvent_AgentMessage,
		*leapmuxv1.AgentEvent_StreamChunk,
		*leapmuxv1.AgentEvent_StreamEnd,
		*leapmuxv1.AgentEvent_MessageError,
		*leapmuxv1.AgentEvent_CatchUpStart,
		*leapmuxv1.AgentEvent_CatchUpComplete:
		return classContent
	default:
		// An unclassified arm must not silently reach a backgrounded tab.
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

// broadcast fans resp out to every channel subscribed to entityID that is
// eligible for class.
//
// For classContent it short-circuits BEFORE the snapshot when no channel holds
// the entity in FULL mode, so a worker whose tabs are all backgrounded pays
// neither the snapshot nor the marshal for chat deltas / terminal bytes. This
// is the single interest gate: callers (e.g. makeTerminalOutputFn) do not need
// their own FULL-mode check, and adding a new content event does not require
// remembering to gate it. Marshal is still lazy per-watcher, so a
// mixed FULL/NOTIFY audience only encodes once the first eligible watcher is
// reached.
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
		// Debug when the connection was simply going away. boundSender.SendStream
		// has already logged this exact failure at the level it deserves; warning
		// again here made one disconnect cost one WARN per open watcher, which is
		// the noise the classification exists to remove. A transport error on a
		// LIVE connection is still a warning -- transportDead is true for that
		// too, which is why the level comes from channel.IsTransportTeardown and
		// not from transportDead.
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

	// ownerMu guards channelOwners / nextSession. A watchSession claims a
	// channel via BeginSession; UnwatchSession is a no-op once a successor
	// has claimed the same channelID, so a predecessor run-defer cannot
	// wipe the successor's registrations.
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

// BeginSession claims channelID for a new watch session and returns its id.
// Any prior session on the same channel stops being the owner, so its later
// UnwatchSession cannot clear the new session's watches. Also drops any
// leftover registrations from a predecessor that has not torn down yet —
// promotion detection snapshots the registry, and a stale FULL from the
// previous stream would suppress catch-up on the new one.
func (m *WatcherManager) BeginSession(channelID string) uint64 {
	m.ownerMu.Lock()
	m.nextSession++
	id := m.nextSession
	m.channelOwners[channelID] = id
	m.ownerMu.Unlock()
	m.agents.unwatchAll(channelID)
	m.terminals.unwatchAll(channelID)
	return id
}

func (m *WatcherManager) isOwner(channelID string, sessionID uint64) bool {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	return m.channelOwners[channelID] == sessionID
}

// SetAgentWatchesForSession makes channelID's agent subscriptions exactly
// entries, but only if sessionID still owns channelID — a cancelled or
// superseded watchSession cannot re-register after UnwatchAll.
func (m *WatcherManager) SetAgentWatchesForSession(channelID string, sessionID uint64, entries []watchEntry, sender channel.ResponseWriter) {
	if !m.isOwner(channelID, sessionID) {
		return
	}
	m.agents.setWatches(channelID, entries, sender)
}

// SetTerminalWatchesForSession makes channelID's terminal subscriptions exactly
// entries, gated on session ownership (see SetAgentWatchesForSession).
func (m *WatcherManager) SetTerminalWatchesForSession(channelID string, sessionID uint64, entries []watchEntry, sender channel.ResponseWriter) {
	if !m.isOwner(channelID, sessionID) {
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

// UnwatchAll removes all subscriptions for a given channel unconditionally
// (channel close / ReleaseLocalStream). Also clears session ownership.
func (m *WatcherManager) UnwatchAll(channelID string) {
	m.ownerMu.Lock()
	delete(m.channelOwners, channelID)
	m.ownerMu.Unlock()
	m.agents.unwatchAll(channelID)
	m.terminals.unwatchAll(channelID)
}

// UnwatchSession removes subscriptions for channelID only if sessionID still
// owns it. Safe for watchSession OnCancel / run defer when a successor may
// already have claimed the channel.
func (m *WatcherManager) UnwatchSession(channelID string, sessionID uint64) {
	m.ownerMu.Lock()
	if m.channelOwners[channelID] != sessionID {
		m.ownerMu.Unlock()
		return
	}
	delete(m.channelOwners, channelID)
	m.ownerMu.Unlock()
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
