package kimi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
)

// kimiEvent is the part of every event payload the dispatcher reads: its type,
// and the agent it belongs to. The payload is the whole event, and the worker
// persists it verbatim -- its `type` repeats the frame's.
type kimiEvent struct {
	Type    string `json:"type"`
	AgentID string `json:"agentId"`
	// Raw is the payload's own bytes, which is what a persisted row holds.
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
// A frame of another session is dropped: after a context clear the previous
// session stays loaded in the server, and a late event of it must not reach the
// new transcript. A global frame of the server that states another session is
// dropped the same way. One that states no session reaches dispatchEvent, which
// ignores every global event type.
func (a *Agent) handleFrame(frame kimiFrame) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()

	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	if frame.SessionID != "" && frame.SessionID != current {
		return
	}
	event, ok := parseKimiEvent(frame)
	if !ok {
		slog.Debug("kimi frame carried no event", "agent_id", a.AgentID(), "type", frame.Type)
		return
	}
	a.dispatchEvent(event)
}

// dispatchEvent routes one event.
//
// The switch lists EVERY event type the server declares, including the ones the
// provider ignores, each with its reason. A type added by a later release then
// reaches the default branch and is logged, instead of being absorbed by a branch
// that looks deliberate.
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
	case contracts.KimiEventTurnStepRetrying,
		contracts.KimiEventCompactionStarted,
		contracts.KimiEventCompactionCompleted,
		contracts.KimiEventCompactionBlocked,
		contracts.KimiEventCompactionCancelled,
		contracts.KimiEventWarning,
		contracts.KimiEventTaskNotified:
		a.persistEventNotification(event)
	case contracts.KimiEventError:
		a.handleError(event)
	case contracts.KimiEventAgentStatusUpdated:
		a.handleStatusUpdated(event)
	case contracts.KimiEventGoalUpdated:
		a.handleGoalUpdated(event)
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
	case contracts.KimiEventApprovalRequested:
		a.handleApprovalRequested(event)
	case contracts.KimiEventApprovalResolved:
		a.handleApprovalResolved(event)
	case contracts.KimiEventQuestionRequested:
		a.handleQuestionRequested(event)
	case contracts.KimiEventQuestionAnswered, contracts.KimiEventQuestionDismissed:
		a.handleQuestionResolved(event)

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
		// The prompt lifecycle repeats what turn.started and turn.ended state.
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
		// The engine's own copies of the approval events. The server's
		// event.approval.* pair is the actionable one.
	case contracts.KimiEventPlanRevision:
		// A plan file revision. The plan reaches LeapMux through its approval.
	case contracts.KimiEventHookResult, contracts.KimiEventMcpServerStatus,
		contracts.KimiEventSkillActivated, contracts.KimiEventPluginCommandActivated,
		contracts.KimiEventToolListUpdated, contracts.KimiEventTowerInboxSent:
		// Engine bookkeeping the conversation does not show.
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
		// The server's global events, for its own web UI. The session's busy flag
		// is turn.started and turn.ended.
	default:
		slog.Debug("kimi unknown event type", "agent_id", a.AgentID(), "type", event.Type)
	}
}
