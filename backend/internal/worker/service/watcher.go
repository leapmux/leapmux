// Package service watcher provides a fan-out event manager for broadcasting
// WatchEventsResponse messages to subscribed E2EE channel clients.
package service

import (
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// EventWatcher represents a subscribed frontend E2EE channel.
type EventWatcher struct {
	ChannelID string
	Sender    *channel.Sender
}

// WatcherManager manages subscriptions for agent and terminal events.
// Events are broadcast to all watchers as InnerStreamMessage frames
// containing serialized WatchEventsResponse payloads.
type WatcherManager struct {
	mu        sync.RWMutex
	agents    map[string][]*EventWatcher // agentID -> watchers
	terminals map[string][]*EventWatcher // terminalID -> watchers
}

// NewWatcherManager creates a new WatcherManager.
func NewWatcherManager() *WatcherManager {
	return &WatcherManager{
		agents:    make(map[string][]*EventWatcher),
		terminals: make(map[string][]*EventWatcher),
	}
}

// WatchAgent registers a watcher for agent events.
// If a watcher with the same channel ID is already registered for this
// agent, its sender is updated to the new one so that live events are
// routed through the latest WatchEvents stream.
func (m *WatcherManager) WatchAgent(agentID string, w *EventWatcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.agents[agentID] {
		if existing.ChannelID == w.ChannelID {
			existing.Sender = w.Sender
			return
		}
	}
	m.agents[agentID] = append(m.agents[agentID], w)
}

// WatchTerminal registers a watcher for terminal events.
// If a watcher with the same channel ID is already registered for this
// terminal, its sender is updated to the new one so that live events
// are routed through the latest WatchEvents stream.
func (m *WatcherManager) WatchTerminal(terminalID string, w *EventWatcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.terminals[terminalID] {
		if existing.ChannelID == w.ChannelID {
			existing.Sender = w.Sender
			return
		}
	}
	m.terminals[terminalID] = append(m.terminals[terminalID], w)
}

// UnwatchAll removes all subscriptions for a given channel.
func (m *WatcherManager) UnwatchAll(channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for agentID, watchers := range m.agents {
		filtered := watchers[:0]
		for _, w := range watchers {
			if w.ChannelID != channelID {
				filtered = append(filtered, w)
			}
		}
		if len(filtered) == 0 {
			delete(m.agents, agentID)
		} else {
			m.agents[agentID] = filtered
		}
	}
	for terminalID, watchers := range m.terminals {
		filtered := watchers[:0]
		for _, w := range watchers {
			if w.ChannelID != channelID {
				filtered = append(filtered, w)
			}
		}
		if len(filtered) == 0 {
			delete(m.terminals, terminalID)
		} else {
			m.terminals[terminalID] = filtered
		}
	}
}

// BroadcastAgentEvent sends an AgentEvent to all watchers of the given agent.
func (m *WatcherManager) BroadcastAgentEvent(agentID string, event *leapmuxv1.AgentEvent) {
	resp := &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
			AgentEvent: event,
		},
	}
	m.broadcastToWatchers(m.agents, agentID, resp)
}

// BroadcastTerminalEvent sends a TerminalEvent to all watchers of the given terminal.
func (m *WatcherManager) BroadcastTerminalEvent(terminalID string, event *leapmuxv1.TerminalEvent) {
	resp := &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
			TerminalEvent: event,
		},
	}
	m.broadcastToWatchers(m.terminals, terminalID, resp)
}

func (m *WatcherManager) broadcastToWatchers(registry map[string][]*EventWatcher, entityID string, resp *leapmuxv1.WatchEventsResponse) {
	m.mu.RLock()
	raw := registry[entityID]
	seen := make(map[string]bool, len(raw))
	watchers := make([]*EventWatcher, 0, len(raw))
	for _, w := range raw {
		if !seen[w.ChannelID] {
			seen[w.ChannelID] = true
			watchers = append(watchers, w)
		}
	}
	m.mu.RUnlock()

	if len(watchers) == 0 {
		return
	}

	slog.Debug("broadcast stream payload", "payload", protojson.Format(resp))
	payload, err := proto.Marshal(resp)
	if err != nil {
		slog.Error("failed to marshal WatchEventsResponse", "entity_id", entityID, "error", err)
		return
	}

	// Collect dead channel IDs so we can drop them after the send loop. A
	// SendStream error means the underlying channel-RPC stream cannot
	// deliver bytes (transport gone, correlation ID closed, peer dropped),
	// so further broadcasts to this watcher would be lost silently. Drop
	// the registration; the channel layer's eventual transport-level error
	// will surface to the frontend as onError/onEnd, which trips the
	// reconnect loop in useWorkspaceConnection.ts and replays from DB.
	var deadChannelIDs []string
	for _, w := range watchers {
		if err := w.Sender.SendStream(&leapmuxv1.InnerStreamMessage{
			Payload: payload,
		}); err != nil {
			slog.Warn("broadcastToWatchers: SendStream failed; dropping watcher",
				"entity_id", entityID, "channel_id", w.ChannelID, "error", err)
			deadChannelIDs = append(deadChannelIDs, w.ChannelID)
		}
	}

	if len(deadChannelIDs) > 0 {
		m.removeWatchersFromRegistry(registry, entityID, deadChannelIDs)
	}
}

// removeWatchersFromRegistry drops every watcher whose channel ID is in
// channelIDs from registry[entityID]. One lock acquisition + one filter
// pass regardless of how many watchers failed simultaneously.
func (m *WatcherManager) removeWatchersFromRegistry(registry map[string][]*EventWatcher, entityID string, channelIDs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dead := make(map[string]bool, len(channelIDs))
	for _, id := range channelIDs {
		dead[id] = true
	}
	watchers := registry[entityID]
	filtered := watchers[:0]
	for _, w := range watchers {
		if !dead[w.ChannelID] {
			filtered = append(filtered, w)
		}
	}
	if len(filtered) == 0 {
		delete(registry, entityID)
	} else {
		registry[entityID] = filtered
	}
}
