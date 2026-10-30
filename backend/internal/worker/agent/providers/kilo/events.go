package kilo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"

	"github.com/leapmux/leapmux/generated/contracts"
)

func (q *kiloQuestions) handleEvent(ctx context.Context, data []byte) {
	var event struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}
	switch event.Type {
	case contracts.NativeQuestionEventQuestionAsked:
		q.publish(ctx, event.Properties)
	case contracts.NativeQuestionEventQuestionReplied, contracts.NativeQuestionEventQuestionRejected:
		// Answered somewhere else -- another client of the same daemon, or LeapMux
		// itself. Either way the card is stale now.
		q.withdraw(event.Properties)
	default:
		if q.observe != nil {
			q.observe(data)
		}
	}
}

// readEvents reads server-sent events and hands each one's data to handleEvent.
// providerkit.ReadSSE states the event-stream rules, including the discard of
// an event that the stream ends in the middle of: the reconnect that follows
// restates the pending list, so no question is lost with it.
func (q *kiloQuestions) readEvents(ctx context.Context, body io.Reader) error {
	return providerkit.ReadSSE(body, kiloQuestionMaxEvent, func(event providerkit.SSEEvent) {
		q.handleEvent(ctx, event.Data)
	})
}

// handleDaemonEvent reads native events on the daemon bridge goroutine.
// The handler verifies the outer session ID before reading the event payload.
func (b *Agent) handleDaemonEvent(raw json.RawMessage) {
	var event struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	sessionID := providerkit.StringField(event.Properties, "sessionID")
	if sessionID == "" || !b.IsCurrentSession(sessionID) {
		slog.Debug("ignore Kilo event outside the current session", "agent_id", b.AgentID(), "type", event.Type)
		return
	}
	switch event.Type {
	case EventSessionUpdated:
		b.observeKiloGoal(event.Type, event.Properties)
	case kiloEventMessageUpdated:
		b.observeDaemonMessage(event.Properties, sessionID)
	case kiloEventPartUpdated:
		var properties struct {
			Part struct {
				ID        string `json:"id"`
				MessageID string `json:"messageID"`
				SessionID string `json:"sessionID"`
				kiloTaskPart
			} `json:"part"`
		}
		if json.Unmarshal(event.Properties, &properties) == nil && (properties.Part.SessionID == "" || properties.Part.SessionID == sessionID) {
			b.observeDaemonPart(properties.Part.ID, properties.Part.MessageID)
			b.observeTaskPart(sessionID, properties.Part.kiloTaskPart)
		}
	case kiloEventPartDelta:
		if providerkit.StringField(event.Properties, "field") == "text" && providerkit.StringField(event.Properties, "delta") != "" {
			b.retryResolution.Resolve(b.Sink())
		}
		b.observeDaemonPart(providerkit.StringField(event.Properties, "partID"), providerkit.StringField(event.Properties, "messageID"))
	case contracts.KiloEventSessionCompacted:
		b.FlushBufferedOutputOffReader(agent.MessageCompletionComplete)
		b.persistDaemonNotification(raw, sessionID)
		providerkit.EndCompaction(b.Sink())
	case kiloEventSessionError:
		b.holdSessionError(raw, event.Properties)
	case contracts.KiloEventSessionStatus:
		var properties struct {
			Status struct {
				Type string `json:"type"`
				// Next is the instant of the daemon's next attempt, in epoch
				// milliseconds. A retry status states it. It stays raw: the
				// daemon adds an unrounded retry-after-ms wait, so the value
				// can hold a fraction, and a typed field would fail this
				// whole decode and drop the retry handling below.
				Next json.RawMessage `json:"next"`
			} `json:"status"`
		}
		if json.Unmarshal(event.Properties, &properties) != nil {
			return
		}
		switch properties.Status.Type {
		case contracts.KiloStatusTypeRetry:
			// The parts that the daemon already reported belong to the request
			// that just failed: the retried request writes its own parts. The
			// daemon emits no message.updated for an assistant message before
			// its request completes, so lastAssistant alone cannot find the
			// message here; every part's owner names it.
			b.daemonMu.Lock()
			if b.lastAssistant != "" {
				b.daemonMessageLocked(b.lastAssistant).retried = true
			}
			for partID, messageID := range b.daemonParts {
				message := b.daemonMessageLocked(messageID)
				message.retried = true
				if message.failedParts == nil {
					message.failedParts = make(map[string]struct{})
				}
				message.failedParts[partID] = struct{}{}
			}
			b.daemonMu.Unlock()
			b.persistDaemonNotification(raw, sessionID)
			b.retryResolution.NoteRetry()
			b.stopForLongWait(raw, properties.Status.Next)
		case "idle":
			b.retryResolution.Reset()
			providerkit.EndCompaction(b.Sink())
		}
	}
}
