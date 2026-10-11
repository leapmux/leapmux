package acp

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
)

// acpSessionUpdates buffers session/update notifications during initialization and session replacement.
// Its mutex serializes identity validation, replay, and dispatch.
type acpSessionUpdates struct {
	mu        sync.Mutex
	buffering bool
	// pending holds the params of each buffered notification, copied from the reader's line.
	pending []json.RawMessage
	// replayCutoff is the length of pending at the moment the reader routed the
	// session/load reply, or -1 when the buffered stream covers no load reply.
	// The Agent Client Protocol sends a loaded session's history before that
	// reply, and the Worker already stores that history as its own transcript,
	// so the buffered conversation below the cutoff is replay, not live output.
	// session/resume sends no replay, so it sets no cutoff.
	replayCutoff int
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
	defer b.sessionUpdates.mu.Unlock()
	if !b.sessionUpdates.buffering {
		return
	}
	pending := b.sessionUpdates.pending
	cutoff := b.sessionUpdates.replayCutoff
	b.sessionUpdates.pending = nil
	b.sessionUpdates.buffering = false
	b.sessionUpdates.replayCutoff = -1
	for index, params := range pending {
		if index < cutoff && isConversationReplay(params) {
			slog.Debug("Drop ACP session replay covered by the load reply", "provider", b.ProviderName(), "agent_id", b.AgentID())
			continue
		}
		b.dispatchACPSessionUpdate(params)
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
	acpUpdatePlan:                        true,
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
	for _, params := range b.sessionUpdates.pending {
		var header struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(params, &header) == nil && header.SessionID == current {
			b.dispatchACPSessionUpdate(params)
		} else {
			remaining = append(remaining, params)
		}
	}
	b.sessionUpdates.pending = remaining
}

// handleACPSessionUpdate holds sessionUpdates.mu across identity validation and dispatch.
// During a transition, it copies the notification into the pending queue.
func (b *Base) handleACPSessionUpdate(params json.RawMessage) {
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	if b.sessionUpdates.buffering {
		b.sessionUpdates.pending = append(b.sessionUpdates.pending, append(json.RawMessage(nil), params...))
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
		b.hooks.SessionNotificationMetadata(wrapper.Meta)
	}
	b.handleACPUpdate(wrapper.Update)
}
