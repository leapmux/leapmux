package acp

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// acpSessionUpdates buffers session/update notifications during initialization and session replacement.
// Its mutex serializes identity validation, replay, and dispatch.
type acpSessionUpdates struct {
	mu        sync.Mutex
	buffering bool
	// pending holds the params of each buffered notification, copied from the reader's line.
	pending []json.RawMessage
}

func (b *Base) beginSessionUpdates() {
	b.sessionUpdates.mu.Lock()
	b.sessionUpdates.buffering = true
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
	b.sessionUpdates.pending = nil
	b.sessionUpdates.buffering = false
	for _, params := range pending {
		b.dispatchACPSessionUpdate(params)
	}
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
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrapper); err != nil {
		slog.Warn("Read ACP session update", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		return
	}
	if current := b.CurrentSessionID(); current != "" && wrapper.SessionID != current {
		slog.Debug("Ignore ACP update from another session", "provider", b.ProviderName(), "agent_id", b.AgentID(), "session_id", wrapper.SessionID)
		return
	}
	if len(wrapper.Update) == 0 {
		return
	}
	b.handleACPUpdate(wrapper.Update)
}
