package kimi

import (
	"encoding/json"
	"fmt"
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
// A previous session can remain loaded after a context clear. Its late events must not reach the new transcript.
// The global session has a separate warning reader. Events without a session ID reach the normal dispatcher.
func (a *Agent) handleFrame(frame kimiFrame) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
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
	a.dispatchEvent(event)
}

// handleGlobalFrame keeps config warnings from this server launch. dispatchMu serializes its writes.
// Other global events update the native server's own session and configuration lists.
func (a *Agent) handleGlobalFrame(frame kimiFrame) {
	// The startup replay reads early warnings again after the sink receives its native session ID.
	if a.IsDiscardingOutput() || a.sink.MessageSessionID() == "" {
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
	keys := make(map[string]struct{})
	for _, warning := range warnings {
		domain := providerkit.StringField(warning, "domain")
		message := providerkit.StringField(warning, "message")
		if strings.TrimSpace(message) == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s%s", len(domain), domain, message)
		if _, exists := a.storedConfigWarnings[key]; exists {
			continue
		}
		if _, exists := keys[key]; exists {
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
		a.storedConfigWarnings = make(map[string]struct{})
	}
	for key := range keys {
		a.storedConfigWarnings[key] = struct{}{}
	}
}

// dispatchEvent routes one event.
//
// The switch states the verdict for every known event and the reason for each internal event.
// The default preserves unknown events as separate message rows.
func (a *Agent) dispatchEvent(event kimiEvent) {
	if a.holdForLink(event) {
		return
	}
	switch event.Type {
	case contracts.KimiEventTurnStarted:
		a.handleTurnStarted(event)
	case contracts.KimiEventTurnEnded:
		a.handleTurnEnded(event)
	case contracts.KimiEventAssistantDelta:
		a.handleTextDelta(event, false)
	case contracts.KimiEventThinkingDelta:
		a.handleTextDelta(event, true)
	case contracts.KimiEventToolCallDelta:
		a.handleToolCallDelta(event)
	case contracts.KimiEventToolCallStarted:
		a.handleToolCallStarted(event)
	case contracts.KimiEventToolProgress:
		a.handleToolProgress(event)
	case contracts.KimiEventToolResult:
		a.handleToolResult(event)
	case contracts.KimiEventTurnStepCompleted:
		a.handleStepCompleted(event)
	case contracts.KimiEventCompactionStarted:
		a.handleCompactionStarted(event)
	case contracts.KimiEventCompactionBlocked:
		a.handleCompactionBlocked(event)
	case contracts.KimiEventCompactionCompleted,
		contracts.KimiEventCompactionCancelled:
		a.handleCompactionFinished(event)
	case contracts.KimiEventTurnStepRetrying:
		a.handleStepRetrying(event)
	case contracts.KimiEventWarning,
		contracts.KimiEventTaskNotified:
		a.persistRunEvent(event)
	case contracts.KimiEventError:
		a.handleError(event)
	case contracts.KimiEventAgentStatusUpdated:
		a.handleStatusUpdated(event)
	case contracts.KimiEventGoalUpdated:
		a.handleGoalUpdated(event)
	case contracts.KimiEventHookResult:
		var hook struct {
			Blocked bool `json:"blocked"`
		}
		if event.decode(&hook) && hook.Blocked {
			a.persistRunEvent(event)
		}
		// A passed hook is an engine record. A blocked hook states why the prompt did not run.
	case contracts.KimiEventSubagentSpawned:
		a.handleSubagentSpawned(event)
	case contracts.KimiEventSubagentCompleted,
		contracts.KimiEventSubagentFailed,
		contracts.KimiEventSubagentCancelled:
		a.handleSubagentEnded(event)
	case contracts.KimiEventTaskStarted:
		a.handleTaskStarted(event)
	case contracts.KimiEventTaskTerminated:
		a.handleTaskTerminated(event)
	case contracts.KimiEventSubagentStarted, contracts.KimiEventSubagentSuspended:
		// A subagent's own turn.started reports that it runs. A swarm member that
		// a rate limit suspended keeps its row Running: the swarm runs it again in
		// a retry turn, and its subagent.* event ends the row
		// (handleChildTurnEnded). The spawn and the end are what the registry
		// needs.
	case contracts.KimiEventTurnStepStarted, contracts.KimiEventTurnStepInterrupted:
		// A step boundary moves nothing LeapMux shows. An interrupted step is
		// followed by the turn.ended that states the outcome.
	case contracts.KimiEventTurnSteer, contracts.KimiEventPromptSteered:
		// The steered text is the message the user sent through LeapMux, which
		// the worker already recorded as the user's own row.
	case contracts.KimiEventPromptSubmitted, contracts.KimiEventPromptQueued,
		contracts.KimiEventPromptStarted, contracts.KimiEventPromptCompleted,
		contracts.KimiEventPromptAborted:
		// The POST reply states a blocked outcome. Native turn events own every turn that actually starts.
	case contracts.KimiEventContextSpliced, contracts.KimiEventContextUndone:
		// The engine's own context edits: injected reminders and undo. They are
		// the model's input, not the conversation.
	case contracts.KimiEventAgentCreated, contracts.KimiEventAgentDisposed:
		// An agent's lifetime in the engine. A subagent's transcript begins at its
		// spawn, which names its parent tool call.
	case contracts.KimiEventBackgroundTaskStarted, contracts.KimiEventBackgroundTaskTerminated:
		// The server's copies of task.started and task.terminated.
	case contracts.KimiEventCronFired:
		// The turn it starts states the cron origin and the prompt, and that
		// turn.started is what the transcript records.
	case contracts.KimiEventPermissionApprovalRequested, contracts.KimiEventPermissionApprovalResolved:
		// Engine records describe approvals. Separate interaction frames own
		// the controls that a client can answer.
	case contracts.KimiEventPlanRevision:
		// A plan file revision. The plan reaches LeapMux through its approval.
	case contracts.KimiEventMcpServerStatus,
		contracts.KimiEventPluginCommandActivated,
		contracts.KimiEventToolListUpdated, contracts.KimiEventTowerInboxSent:
		// Engine bookkeeping the conversation does not show.
	case contracts.KimiEventSkillActivated:
		// The Skill tool card states the activation. A slash activation is already the user's own message.
	case contracts.KimiEventShellStarted, contracts.KimiEventShellOutput, contracts.KimiEventShellCompleted:
		// The TUI's own `!` shell, which LeapMux does not drive.
	case contracts.KimiEventSessionMetaUpdated:
		// LeapMux titles an agent from the user's first message and its own
		// renaming flow. The server's title would fight that.
	case contracts.KimiEventSessionCreated, contracts.KimiEventSessionArchived,
		contracts.KimiEventSessionDeleted, contracts.KimiEventSessionWorkChanged,
		contracts.KimiEventSessionStatusChanged,
		contracts.KimiEventWorkspaceCreated, contracts.KimiEventWorkspaceUpdated,
		contracts.KimiEventWorkspaceDeleted,
		contracts.KimiEventConfigChanged, contracts.KimiEventConfigWarning,
		contracts.KimiEventModelCatalogChanged, contracts.KimiEventPluginChanged,
		contracts.KimiEventCapabilityChanged, contracts.KimiEventUnitChanged:
		// handleGlobalFrame handles the global envelope before this dispatcher runs.
		// Native turn events own the busy flag. Session and configuration records supply no transcript row.
	default:
		a.persistRunEvent(event)
	}
}
