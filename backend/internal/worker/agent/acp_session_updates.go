package agent

import (
	"encoding/json"
	"log/slog"
	"sync"
)

type acpPendingSessionUpdate struct {
	params json.RawMessage
	extra  acpSessionUpdateHandler
}

// acpSessionUpdates buffers session/update notifications during initialization and session replacement.
// Its mutex serializes identity validation, replay, and dispatch.
type acpSessionUpdates struct {
	mu        sync.Mutex
	buffering bool
	pending   []acpPendingSessionUpdate
}

func (b *acpBase) beginSessionUpdates() {
	b.sessionUpdates.mu.Lock()
	b.sessionUpdates.buffering = true
	b.sessionUpdates.mu.Unlock()
}

// finishSessionUpdates replays buffered notifications before live dispatch can overtake them.
// Call it without b.mu or turnMu because dispatch can acquire both locks.
func (b *acpBase) finishSessionUpdates() {
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	if !b.sessionUpdates.buffering {
		return
	}
	pending := b.sessionUpdates.pending
	b.sessionUpdates.pending = nil
	b.sessionUpdates.buffering = false
	for _, update := range pending {
		b.dispatchACPSessionUpdate(update.params, update.extra)
	}
}

// flushPreviousSessionUpdates dispatches buffered updates for the current session.
// The caller holds sessionUpdates.mu and keeps the current session ID unchanged.
func (b *acpBase) flushPreviousSessionUpdates() {
	current := b.currentSessionID()
	remaining := b.sessionUpdates.pending[:0]
	for _, update := range b.sessionUpdates.pending {
		var header struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(update.params, &header) == nil && header.SessionID == current {
			b.dispatchACPSessionUpdate(update.params, update.extra)
		} else {
			remaining = append(remaining, update)
		}
	}
	b.sessionUpdates.pending = remaining
}

// handleACPSessionUpdate holds sessionUpdates.mu across identity validation and dispatch.
// During a transition, it copies the notification into the pending queue.
func (b *acpBase) handleACPSessionUpdate(params json.RawMessage, extra acpSessionUpdateHandler) {
	b.sessionUpdates.mu.Lock()
	defer b.sessionUpdates.mu.Unlock()
	if b.sessionUpdates.buffering {
		b.sessionUpdates.pending = append(b.sessionUpdates.pending, acpPendingSessionUpdate{
			params: append(json.RawMessage(nil), params...), extra: extra,
		})
		return
	}
	b.dispatchACPSessionUpdate(params, extra)
}

func (b *acpBase) dispatchACPSessionUpdate(params json.RawMessage, extra acpSessionUpdateHandler) {
	var wrapper struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		slog.Warn("Read ACP session update", "provider", b.providerName, "agent_id", b.agentID, "error", err)
		return
	}
	if current := b.currentSessionID(); current != "" && wrapper.SessionID != current {
		slog.Debug("Ignore ACP update from another session", "provider", b.providerName, "agent_id", b.agentID, "session_id", wrapper.SessionID)
		return
	}
	if len(wrapper.Update) == 0 {
		return
	}
	b.handleACPUpdate(wrapper.Update, extra)
}
