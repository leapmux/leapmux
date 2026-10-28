package acp

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
)

// acpSessionUpdates buffers session/update notifications during initialization and session replacement.
// Its mutex serializes session updates and identity checks.
// Off-reader buffer flushes use the same mutex.
type acpSessionUpdates struct {
	mu        sync.Mutex
	buffering bool
	// pending keeps standard and provider notifications in received order.
	pending []bufferedSessionUpdate
	// replayCutoff is the length of pending at the moment the reader routed the
	// session/load reply, or -1 when the buffered stream covers no load reply.
	// The Agent Client Protocol sends a loaded session's history before that
	// reply, and the Worker already stores that history as its own transcript,
	// so the buffered conversation below the cutoff is replay, not live output.
	// session/resume sends no replay, so it sets no cutoff.
	replayCutoff int
}

type bufferedSessionUpdate struct {
	sessionID     string
	rootSessionID string
	raw           json.RawMessage
	// dispatch is nil for standard session/update params. A provider supplies
	// its concrete handler for an extension notification's unchanged frame.
	dispatch       func(string, json.RawMessage)
	resolveSession func(json.RawMessage) (string, bool)
}

// SessionNotification supplies a provider's concrete source resolver and
// handler. Shared code never interprets the provider's notification shape.
type SessionNotification struct {
	Raw            json.RawMessage
	ResolveSession func(json.RawMessage) (string, bool)
	Dispatch       func(string, json.RawMessage)
}

// RouteSessionNotification serializes a provider notification with session
// replacement. Once its source resolves, that identity remains fixed. An
// unresolved source can wait for an earlier buffered metadata notification.
// The handler runs on the reader or during ordered replay. It must not wait
// for another native request, acquire sessionMu or enter childDispatch.
func (b *Base) RouteSessionNotification(notification SessionNotification) bool {
	if notification.ResolveSession == nil || notification.Dispatch == nil {
		return false
	}
	b.sessionUpdates.mu.Lock()
	defer func() {
		b.sessionUpdates.mu.Unlock()
		b.FlushRegistryObservations()
	}()
	update := bufferedSessionUpdate{raw: notification.Raw, dispatch: notification.Dispatch, resolveSession: notification.ResolveSession}
	b.resolveBufferedSessionUpdate(&update)
	if b.sessionUpdates.buffering {
		update.raw = append(json.RawMessage(nil), notification.Raw...)
		b.sessionUpdates.pending = append(b.sessionUpdates.pending, update)
		return true
	}
	b.dispatchBufferedSessionUpdate(update)
	return true
}

func (b *Base) resolveBufferedSessionUpdate(update *bufferedSessionUpdate) bool {
	if update.dispatch == nil || update.sessionID != "" {
		return true
	}
	sessionID, known := update.resolveSession(update.raw)
	if !known || sessionID == "" {
		return false
	}
	update.sessionID = sessionID
	update.rootSessionID, _ = b.SessionRoot(sessionID)
	return true
}

func (b *Base) dispatchBufferedSessionUpdate(update bufferedSessionUpdate) {
	if !b.resolveBufferedSessionUpdate(&update) {
		return
	}
	if !b.ServesSession(update.sessionID) {
		return
	}
	if update.rootSessionID != "" {
		rootSessionID, served := b.SessionRoot(update.sessionID)
		if !served || rootSessionID != update.rootSessionID {
			return
		}
	}
	if update.dispatch != nil {
		update.dispatch(update.sessionID, update.raw)
		return
	}
	b.dispatchACPSessionUpdate(update.raw)
}

func (b *Base) beginSessionUpdates() {
	b.sessionUpdates.mu.Lock()
	b.sessionUpdates.buffering = true
	b.sessionUpdates.replayCutoff = -1
	b.sessionUpdates.mu.Unlock()
}

// markSessionReplayCutoff records which part of the buffered stream preceded the
// session/load reply. Call it on the reader goroutine as it routes the reply
// (see Base.SendRequestObserved), while buffering is still on. The reader has
// buffered no later line at that point, so no live line can fall below the mark.
func (b *Base) markSessionReplayCutoff() {
	b.sessionUpdates.mu.Lock()
	if b.sessionUpdates.buffering && b.sessionUpdates.replayCutoff < 0 {
		b.sessionUpdates.replayCutoff = len(b.sessionUpdates.pending)
	}
	b.sessionUpdates.mu.Unlock()
}

// finishSessionUpdates replays buffered notifications before live dispatch can overtake them.
// Call it without b.Mu or turnMu because dispatch can acquire both locks.
func (b *Base) finishSessionUpdates() {
	b.sessionUpdates.mu.Lock()
	defer func() {
		b.sessionUpdates.mu.Unlock()
		b.FlushRegistryObservations()
	}()
	if !b.sessionUpdates.buffering {
		return
	}
	pending := b.sessionUpdates.pending
	cutoff := b.sessionUpdates.replayCutoff
	b.sessionUpdates.pending = nil
	b.sessionUpdates.buffering = false
	b.sessionUpdates.replayCutoff = -1
	for index, update := range pending {
		if index < cutoff && update.dispatch == nil && isConversationReplay(update.raw) {
			slog.Debug("Drop ACP session replay covered by the load reply", "provider", b.ProviderName(), "agent_id", b.AgentID())
			continue
		}
		b.dispatchBufferedSessionUpdate(update)
	}
}

// conversationReplayKinds states the update kinds that draw conversation rows.
// Everything a session/load reply already covers of these is history the Worker
// stored when it first ran: messages, thoughts, tool calls and their updates,
// and the plan panel. Usage, mode and session metadata state the session's
// current state, so they stay live even below the cutoff.
var conversationReplayKinds = map[string]bool{
	contracts.ACPUpdateUserMessageChunk:  true,
	contracts.ACPUpdateAgentMessageChunk: true,
	contracts.ACPUpdateAgentThoughtChunk: true,
	contracts.ACPUpdateToolCall:          true,
	contracts.ACPUpdateToolCallUpdate:    true,
	contracts.ACPUpdatePlan:              true,
}

// isConversationReplay reports whether one buffered notification is a
// conversation update. Malformed params count as live: a broken frame is a
// defect to see, not history to drop.
func isConversationReplay(params json.RawMessage) bool {
	var wrapper struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &wrapper) != nil {
		return false
	}
	return conversationReplayKinds[wrapper.Update.SessionUpdate]
}

// flushPreviousSessionUpdates dispatches buffered updates for the current session.
// The caller holds sessionUpdates.mu and keeps the current session ID unchanged.
func (b *Base) flushPreviousSessionUpdates() {
	current := b.CurrentSessionID()
	remaining := b.sessionUpdates.pending[:0]
	for _, update := range b.sessionUpdates.pending {
		if !b.resolveBufferedSessionUpdate(&update) {
			remaining = append(remaining, update)
			continue
		}
		root, served := b.SessionRoot(update.sessionID)
		if update.sessionID == current || served && root == current {
			b.dispatchBufferedSessionUpdate(update)
		} else {
			remaining = append(remaining, update)
		}
	}
	b.sessionUpdates.pending = remaining
}

// handleACPSessionUpdate holds sessionUpdates.mu across identity validation and dispatch.
// During a transition, it copies the notification into the pending queue.
func (b *Base) handleACPSessionUpdate(params json.RawMessage) {
	b.sessionUpdates.mu.Lock()
	defer func() {
		b.sessionUpdates.mu.Unlock()
		b.FlushRegistryObservations()
	}()
	if b.sessionUpdates.buffering {
		var header struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &header)
		rootSessionID, _ := b.SessionRoot(header.SessionID)
		b.sessionUpdates.pending = append(b.sessionUpdates.pending, bufferedSessionUpdate{
			sessionID:     header.SessionID,
			rootSessionID: rootSessionID,
			raw:           append(json.RawMessage(nil), params...),
		})
		return
	}
	b.dispatchACPSessionUpdate(params)
}

func (b *Base) dispatchACPSessionUpdate(params json.RawMessage) {
	var wrapper struct {
		SessionID string                     `json:"sessionId"`
		Update    json.RawMessage            `json:"update"`
		Meta      map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		slog.Warn("Read ACP session update", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		return
	}
	if len(wrapper.Update) == 0 {
		return
	}
	if b.hooks.SessionUpdateHandler != nil {
		if owner, found := b.sessionUpdateOwner(wrapper.SessionID); found &&
			b.hooks.SessionUpdateHandler(wrapper.SessionID, owner, wrapper.Update) {
			return
		}
	}
	if current := b.CurrentSessionID(); current != "" && wrapper.SessionID != current {
		// A subagent that runs in a session of its own reaches its transcript.
		// Every other session is one this agent no longer serves.
		if rowKey := b.childSessionRow(wrapper.SessionID); rowKey != "" && b.FeedChildUpdate(rowKey, wrapper.Update) {
			return
		}
		slog.Debug("Ignore ACP update from another session", "provider", b.ProviderName(), "agent_id", b.AgentID(), "session_id", wrapper.SessionID)
		return
	}
	if b.hooks.SessionNotificationMetadata != nil && len(wrapper.Meta) > 0 {
		var kind struct {
			SessionUpdate string `json:"sessionUpdate"`
		}
		if json.Unmarshal(wrapper.Update, &kind) != nil {
			kind.SessionUpdate = ""
		}
		b.hooks.SessionNotificationMetadata(kind.SessionUpdate, wrapper.Meta)
	}
	b.handleACPUpdate(wrapper.Update)
}
