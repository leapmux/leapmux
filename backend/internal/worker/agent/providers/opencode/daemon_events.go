package opencode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	// EventSessionUpdated names the daemon event that carries the complete
	// session info, including its metadata. Family members read their own
	// metadata vocabulary from it through MemberEvents.
	EventSessionUpdated         = "session.updated"
	openCodeEventMessageUpdated = "message.updated"
	openCodeEventPartUpdated    = "message.part.updated"
	openCodeEventPartDelta      = "message.part.delta"
)

type openCodeMessageState struct {
	summary          bool
	retried          bool
	streamedFallback bool
	// failedParts holds the parts that a retried request superseded. The
	// daemon keeps their partial text in its store, so a stored read skips
	// them instead of joining the failed attempt to the answer.
	failedParts map[string]struct{}
	emitted     map[string]string
}

// handleDaemonEvent reads native events on the daemon bridge goroutine.
// Every family consumer uses the outer session ID before it reads the event payload.
func (b *FamilyBase) handleDaemonEvent(raw json.RawMessage) {
	var event struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	sessionID := providerkit.StringField(event.Properties, "sessionID")
	if sessionID == "" || !b.IsCurrentSession(sessionID) {
		slog.Debug("ignore OpenCode event outside the current session", "agent_id", b.AgentID(), "type", event.Type)
		return
	}
	switch event.Type {
	case EventSessionUpdated:
		if b.MemberEvents != nil {
			b.MemberEvents(event.Type, event.Properties)
		}
	case openCodeEventMessageUpdated:
		b.observeDaemonMessage(event.Properties, sessionID)
	case openCodeEventPartUpdated:
		var properties struct {
			Part struct {
				ID        string `json:"id"`
				MessageID string `json:"messageID"`
				SessionID string `json:"sessionID"`
				openCodeTaskPart
			} `json:"part"`
		}
		if json.Unmarshal(event.Properties, &properties) == nil && (properties.Part.SessionID == "" || properties.Part.SessionID == sessionID) {
			b.observeDaemonPart(properties.Part.ID, properties.Part.MessageID)
			b.observeTaskPart(sessionID, properties.Part.openCodeTaskPart)
		}
	case openCodeEventPartDelta:
		if providerkit.StringField(event.Properties, "field") == "text" && providerkit.StringField(event.Properties, "delta") != "" {
			b.retryResolution.Resolve(b.Sink())
		}
		b.observeDaemonPart(providerkit.StringField(event.Properties, "partID"), providerkit.StringField(event.Properties, "messageID"))
	case contracts.OpenCodeEventSessionCompacted:
		b.FlushBufferedOutputOffReader(agent.MessageCompletionComplete)
		b.persistDaemonNotification(raw, sessionID)
		providerkit.EndCompaction(b.Sink())
	case openCodeEventSessionError:
		b.holdSessionError(raw, event.Properties)
	case contracts.OpenCodeEventSessionStatus:
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
		case contracts.OpenCodeStatusTypeRetry:
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

func (b *FamilyBase) persistDaemonNotification(raw json.RawMessage, sessionID string) {
	if _, err := b.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, AgentSessionID: sessionID}); err != nil {
		slog.Error("persist OpenCode daemon notification", "agent_id", b.AgentID(), "error", err)
	}
}

func (b *FamilyBase) daemonMessageLocked(id string) *openCodeMessageState {
	if b.daemonMessages == nil {
		b.daemonMessages = make(map[string]*openCodeMessageState)
	}
	message := b.daemonMessages[id]
	if message == nil {
		message = &openCodeMessageState{emitted: make(map[string]string)}
		b.daemonMessages[id] = message
	}
	return message
}

func (b *FamilyBase) observeDaemonMessage(raw json.RawMessage, sessionID string) {
	var properties struct {
		Info struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Role      string `json:"role"`
			Mode      string `json:"mode"`
			Summary   bool   `json:"summary"`
			Time      struct {
				Completed json.RawMessage `json:"completed"`
			} `json:"time"`
		} `json:"info"`
	}
	if json.Unmarshal(raw, &properties) != nil {
		return
	}
	info := properties.Info
	if info.ID == "" || info.Role != "assistant" || (info.SessionID != "" && info.SessionID != sessionID) {
		return
	}
	b.daemonMu.Lock()
	message := b.daemonMessageLocked(info.ID)
	wasSummary := message.summary
	message.summary = message.summary || info.Summary || info.Mode == "compaction"
	started := !wasSummary && message.summary && !providerkit.RawPresent(info.Time.Completed)
	if !providerkit.RawPresent(info.Time.Completed) {
		b.lastAssistant = info.ID
	}
	b.daemonMu.Unlock()
	if started {
		providerkit.PublishCompaction(b.Sink(), "")
	}
}

func (b *FamilyBase) observeDaemonPart(partID, messageID string) {
	if partID == "" || messageID == "" {
		return
	}
	b.daemonMu.Lock()
	defer b.daemonMu.Unlock()
	if b.daemonParts == nil {
		b.daemonParts = make(map[string]string)
	}
	b.daemonParts[partID] = messageID
	b.daemonMessageLocked(messageID)
}

// resetDaemonGeneration forgets the daemon state of the prompt before, when a
// prompt starts or the session changes.
func (b *FamilyBase) resetDaemonGeneration() {
	b.retryResolution.Reset()
	startedAt := b.Clock().Now()
	b.daemonMu.Lock()
	b.daemonMessages, b.daemonParts, b.lastAssistant = nil, nil, ""
	b.promptStartedAt = startedAt
	b.heldSessionError, b.longWait, b.userStopRequested, b.compactionPrompt = nil, nil, false, false
	b.daemonMu.Unlock()
}

func (b *FamilyBase) clearSessionState() {
	b.resetDaemonGeneration()
	rootSessionID := b.CurrentSessionID()
	b.childSessionsMu.Lock()
	for callID, child := range b.childSessions {
		if child.rootSessionID != rootSessionID {
			delete(b.childSessions, callID)
		}
	}
	b.childSessionsMu.Unlock()
	b.childStopMu.Lock()
	for callID, owner := range b.stoppedChildren {
		if owner != rootSessionID {
			delete(b.stoppedChildren, callID)
		}
	}
	b.childStopMu.Unlock()
}

// captureDaemonCleanup retains entry identities, so a delayed cleanup cannot delete a later turn's entries.
func (b *FamilyBase) captureDaemonCleanup() func() {
	b.daemonMu.Lock()
	previous := maps.Clone(b.daemonMessages)
	b.daemonMu.Unlock()
	if len(previous) == 0 {
		return nil
	}
	return func() {
		b.daemonMu.Lock()
		defer b.daemonMu.Unlock()
		for id, message := range previous {
			if b.daemonMessages[id] != message {
				continue
			}
			delete(b.daemonMessages, id)
			if b.lastAssistant == id {
				b.lastAssistant = ""
			}
		}
		for partID, messageID := range b.daemonParts {
			if _, old := previous[messageID]; old && b.daemonMessages[messageID] == nil {
				delete(b.daemonParts, partID)
			}
		}
	}
}

func (b *FamilyBase) segmentText(kind agent.AssembledMessageKind, id, text string) string {
	if id == "" {
		return text
	}
	sessionID := b.CurrentSessionID()
	b.daemonMu.Lock()
	messageID, partID := id, ""
	if owner := b.daemonParts[id]; owner != "" {
		messageID, partID = owner, id
	}
	message := b.daemonMessages[messageID]
	if message == nil {
		b.daemonMu.Unlock()
		return text
	}
	if message.summary {
		b.daemonMu.Unlock()
		return ""
	}
	if !message.retried || message.streamedFallback {
		b.daemonMu.Unlock()
		return text
	}
	b.daemonMu.Unlock()

	parts, err := b.Questions.readStoredMessage(b.Context(), b.Clock(), sessionID, messageID)
	b.daemonMu.Lock()
	defer b.daemonMu.Unlock()
	if message.streamedFallback {
		return text
	}
	if err == nil {
		var selected string
		selected, err = selectStoredText(message, parts, kind, partID)
		if err == nil {
			return selected
		}
	}
	// A later snapshot cannot identify bytes that the fallback already emitted.
	message.streamedFallback = true
	slog.Warn("OpenCode stored message is unavailable. The transcript keeps streamed text for this message.", "agent_id", b.AgentID(), "message_id", messageID, "error", err)
	return text
}

func selectStoredText(message *openCodeMessageState, parts []openCodeStoredPart, kind agent.AssembledMessageKind, partID string) (string, error) {
	var nativeKind string
	switch kind {
	case agent.AssembledMessageKindText:
		nativeKind = "text"
	case agent.AssembledMessageKindReasoning:
		nativeKind = "reasoning"
	default:
		return "", fmt.Errorf("unsupported model segment kind %q", kind)
	}
	var result strings.Builder
	updates := make(map[string]string)
	for _, part := range parts {
		if part.Type != nativeKind || (partID != "" && part.ID != partID) {
			continue
		}
		if _, failed := message.failedParts[part.ID]; failed {
			continue
		}
		if part.ID == "" {
			return "", fmt.Errorf("a stored %s part has no ID", nativeKind)
		}
		if _, duplicate := updates[part.ID]; duplicate {
			return "", fmt.Errorf("stored part %q repeats", part.ID)
		}
		previous := message.emitted[part.ID]
		if !strings.HasPrefix(part.Text, previous) {
			return "", fmt.Errorf("stored part %q changed text that the transcript already emitted", part.ID)
		}
		result.WriteString(part.Text[len(previous):])
		updates[part.ID] = part.Text
	}
	for id, value := range updates {
		message.emitted[id] = value
	}
	return result.String(), nil
}
