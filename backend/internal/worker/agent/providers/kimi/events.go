package kimi

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kimiEvent identifies an event's type and native agent.
// Raw keeps the event payload before provider-owned filtering.
type kimiEvent struct {
	Type    string `json:"type"`
	AgentID string `json:"agentId"`
	// Raw contains the event payload. Its type repeats the enclosing frame's type.
	Raw json.RawMessage `json:"-"`
}

// parseKimiEvent decodes one event frame's payload. ok is false for a frame
// with no payload type.
func parseKimiEvent(frame kimiFrame) (kimiEvent, bool) {
	if len(frame.Payload) == 0 {
		return kimiEvent{}, false
	}
	var event kimiEvent
	if err := json.Unmarshal(frame.Payload, &event); err != nil || event.Type == "" {
		return kimiEvent{}, false
	}
	event.Raw = append(json.RawMessage(nil), frame.Payload...)
	if event.AgentID == "" {
		event.AgentID = kimiMainAgentID
	}
	return event, true
}

// decode unmarshals the event's payload into v, and logs a payload that does
// not fit.
func (e kimiEvent) decode(v any) bool {
	if err := json.Unmarshal(e.Raw, v); err != nil {
		slog.Warn("kimi event payload does not decode", "type", e.Type, "error", err)
		return false
	}
	return true
}

// handleFrame dispatches one event frame.
//
// Delayed events from another session must not reach the current transcript.
// The global session has a separate warning reader. Events without a session ID reach the normal dispatcher.
func (a *Agent) handleFrame(frame kimiFrame) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.dispatchFrame(frame)
}

// dispatchFrame runs under dispatchMu, including when a new session adopts its initial frames.
func (a *Agent) dispatchFrame(frame kimiFrame) {
	if pending := a.pendingSession; pending != nil && frame.SessionID == pending.sessionID {
		pending.append(frame)
		return
	}
	if frame.SessionID == kimiGlobalSessionID {
		a.handleGlobalFrame(frame)
		return
	}

	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	if frame.SessionID != "" && frame.SessionID != current {
		slog.Debug("ignore a Kimi frame from another session", "agent_id", a.AgentID(), "session_id", frame.SessionID, "type", frame.Type)
		return
	}
	// The global publisher uses the real session ID for session metadata and
	// work changes. Its event namespace is not conversation output.
	if strings.HasPrefix(frame.Type, "event.") || frame.Type == "session.meta.updated" {
		a.handleGlobalFrame(frame)
		return
	}
	if frame.Type == contracts.KimiEventTranscriptOps {
		a.handleTranscriptOperations(frame)
		return
	}
	if frame.Type == contracts.KimiEventTranscriptReset {
		a.handleTranscriptReset(frame)
		return
	}
	if frame.Type == contracts.KimiEventAgent {
		if state, valid := decodeKimiAgentState(frame); valid && state.AgentID == kimiMainAgentID {
			a.foldStatus(state.status())
		}
		return
	}
	if frame.Type == contracts.KimiEventUsage {
		a.handleNativeUsage(frame)
		return
	}
	if frame.Type == contracts.KimiEventGoal {
		a.handleNativeGoal(frame)
		return
	}
	if frame.Type == contracts.KimiEventTodo {
		a.handleNativeTodo(frame)
		return
	}
	if frame.Type == contracts.KimiEventTask || frame.Type == contracts.KimiEventTaskList {
		a.handleNativeTaskFrame(frame)
		return
	}
	if frame.Type == contracts.KimiFrameInteraction {
		if frame.SessionID != "" {
			a.handleInteraction(frame.Raw)
		}
		return
	}
	event, ok := parseKimiEvent(frame)
	if !ok {
		slog.Debug("kimi frame carried no event", "agent_id", a.AgentID(), "type", frame.Type)
		return
	}
	a.persistEvent(a.runSink(a.run(event.AgentID)), event)
}

// handleGlobalFrame keeps config warnings from this server launch. dispatchMu serializes its writes.
// Other global events update the native server's own session and configuration lists.
func (a *Agent) handleGlobalFrame(frame kimiFrame) {
	if a.IsDiscardingOutput() {
		return
	}
	if a.sink.MessageSessionID() == "" {
		if frame.Type == contracts.KimiEventConfigWarning {
			a.pendingConfigWarnings = append(a.pendingConfigWarnings, frame)
		}
		return
	}
	event, ok := parseKimiEvent(frame)
	if !ok || event.Type != contracts.KimiEventConfigWarning {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, frame.Timestamp)
	if err != nil || at.Before(a.serverLaunchedAt) {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(event.Raw, &fields) != nil {
		return
	}
	var warnings []json.RawMessage
	if json.Unmarshal(fields["warnings"], &warnings) != nil {
		return
	}
	var fresh []json.RawMessage
	keys := make(map[kimiConfigWarningKey]struct{})
	for _, warning := range warnings {
		domain := providerkit.StringField(warning, "domain")
		message := providerkit.StringField(warning, "message")
		if strings.TrimSpace(message) == "" {
			continue
		}
		key := kimiConfigWarningKey{domain: domain, message: message}
		if kimiConfigWarningKnown(a.storedConfigWarnings, key) {
			continue
		}
		if kimiConfigWarningKnown(keys, key) {
			continue
		}
		keys[key] = struct{}{}
		fresh = append(fresh, warning)
	}
	if len(fresh) == 0 {
		return
	}
	// Preserve native fields while removing warnings that an earlier frame already supplied.
	fields["warnings"], err = json.Marshal(fresh)
	if err != nil {
		slog.Error("encode Kimi config warnings", "agent_id", a.AgentID(), "error", err)
		return
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		slog.Error("encode the Kimi config warning event", "agent_id", a.AgentID(), "error", err)
		return
	}
	event.Raw = raw
	if !a.persistEvent(a.sink, event) {
		return
	}
	if a.storedConfigWarnings == nil {
		a.storedConfigWarnings = make(map[kimiConfigWarningKey]struct{})
	}
	for key := range keys {
		a.storedConfigWarnings[key] = struct{}{}
	}
}

type kimiConfigWarningKey struct {
	domain  string
	message string
}

// The native validator omits domains. Match its messages to later structured events.
func kimiConfigWarningKnown(keys map[kimiConfigWarningKey]struct{}, key kimiConfigWarningKey) bool {
	if _, present := keys[key]; present {
		return true
	}
	if key.domain != "" {
		_, present := keys[kimiConfigWarningKey{message: key.message}]
		return present
	}
	for previous := range keys {
		if previous.message == key.message {
			return true
		}
	}
	return false
}

// flushStartupConfigWarnings writes early global warnings after session admission.
// The native global stream has no subscription or replay cursor.
func (a *Agent) flushStartupConfigWarnings() {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	pending := a.pendingConfigWarnings
	a.pendingConfigWarnings = nil
	for _, frame := range pending {
		a.handleGlobalFrame(frame)
	}
}
