package cline

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Event dispatch.
//
// The hub's dispatcher goroutine calls handleEvent for each event, in the order
// the daemon published them, and HandleOutput does the same for a test. One
// event's handling runs under dispatchMu.
//
// Cline states the session of each event, and states no agent on the events
// that carry output: a subagent's text and tool calls, and a teammate's, arrive
// among the lead's. The worker routes that output by what it saw before it
// (routeContent, subagent.go).

// handleEvent dispatches one event.
func (a *Agent) handleEvent(event hubEvent) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if event.SessionID == "" && isRequestEvent(event.Event) {
		// Cline states the session of each request. A request with none belongs
		// to no session that the agent drives, so no mode may answer it.
		slog.Warn("cline request states no session", "agent_id", a.AgentID(), "event", event.Event)
		a.declineForeignRequest(event, "The request states no session.")
		return
	}
	if current := a.currentSession(); event.SessionID != "" && event.SessionID != current {
		// An event of a session the agent no longer drives: the session that a
		// context clear or a mode rebuild detached. A request of that session
		// must not wait for an answer that cannot come.
		a.declineForeignRequest(event, "The session ended.")
		return
	}
	switch event.Event {
	case eventRunStarted:
		a.handleRunStarted(event)
	case eventSessionUpdated:
		a.handleSessionUpdated(event)
	case eventIterationStarted:
		a.handleIterationStarted()
	case eventReasoningDelta:
		if t := a.routeContent(); t != nil {
			a.handleReasoningDelta(t, event.Payload)
		}
	case eventAssistantDelta:
		if t := a.routeContent(); t != nil {
			a.handleAssistantDelta(t, event.Payload)
		}
	case contracts.ClineEventAssistantFinished:
		if t := a.routeContent(); t != nil {
			a.handleAssistantFinished(t, event)
		}
	case contracts.ClineEventReasoningFinished:
		if t := a.routeContent(); t != nil {
			a.handleReasoningFinished(t, event)
		}
	case contracts.ClineEventAssistantMedia:
		if t := a.routeContent(); t != nil {
			a.flushReasoning(t, event.SessionID)
			a.persistRow(t, event.Raw, noCompletion)
		}
	case contracts.ClineEventToolStarted:
		a.dispatchToolStarted(event)
	case eventToolUpdated:
		a.dispatchToolUpdated(event)
	case contracts.ClineEventToolFinished:
		a.dispatchToolFinished(event)
	case eventUsageUpdated:
		a.handleUsageUpdated(event.Payload)
	case contracts.ClineEventRunCompleted, contracts.ClineEventRunFailed, contracts.ClineEventRunAborted:
		a.handleRunEnded(event)
	case contracts.ClineEventSessionNotice:
		a.handleSessionNotice(event)
	case contracts.ClineEventTeamProgress:
		a.handleTeamProgress(event)
	case contracts.ClineEventApprovalRequested:
		a.handleApprovalRequested(event)
	case contracts.ClineEventCapabilityRequested:
		a.handleCapabilityRequested(event)
	case eventApprovalResolved, eventCapabilityResolved:
		a.handleControlResolved(event)
	}
}

// handleRunStarted confirms the delivery of the send that started the run.
// A run that no send of this agent started -- another client's -- starts a turn
// that takes no steer.
func (a *Agent) handleRunStarted(event hubEvent) {
	var started struct {
		RequestID string `json:"requestId"`
		ClientID  string `json:"clientId"`
	}
	_ = json.Unmarshal(event.Payload, &started)
	delivered := started.RequestID != "" && a.settleDelivery(started.RequestID, nil)
	a.Mu.Lock()
	// A send that already gave up waiting armed the turn of this run.
	ours := started.RequestID != "" && a.turn.active && a.turn.requestID == started.RequestID
	a.Mu.Unlock()
	if !delivered && !ours {
		a.ensureTurn()
	}
	a.markRunStarted()
}

// markRunStarted records that the turn's run exists, and aborts the run when
// the user interrupted the turn before it started. The caller holds
// dispatchMu, so the abort leaves from its own goroutine.
func (a *Agent) markRunStarted() {
	a.Mu.Lock()
	if !a.turn.active || a.turn.settling {
		a.Mu.Unlock()
		return
	}
	a.turn.runStarted = true
	abort := a.turn.abortOnStart
	a.turn.abortOnStart = false
	if abort {
		a.turn.interruptRequested = true
	}
	sessionID := a.sessionID
	a.Mu.Unlock()
	if abort {
		a.sendInBackground(func() error { return a.abortRun(sessionID) })
	}
}

// handleSessionUpdated follows the session's own run status.
//
//   - `running`: a lead run that Cline starts by itself -- the run that
//     continues after the teammates of an agent team end -- starts no
//     run.started, and its session turns `running`.
//   - `idle`: a turn that has no run (turnState.hasNoRun) ends, because no run
//     end will end it. A turn with a run ends with the run's own end event,
//     which Cline publishes just after the session turns `idle`.
func (a *Agent) handleSessionUpdated(event hubEvent) {
	var updated struct {
		Snapshot struct {
			Status string `json:"status"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(event.Payload, &updated) != nil {
		return
	}
	switch updated.Snapshot.Status {
	case sessionStatusRunning:
		a.ensureTurn()
		a.markRunStarted()
	case sessionStatusIdle:
		a.endRunlessTurnLocked(agent.MessageCompletionComplete, runReasonCompleted)
	}
}

// dispatchToolStarted opens one tool call where its output belongs.
func (a *Agent) dispatchToolStarted(event hubEvent) {
	var call toolEvent
	if json.Unmarshal(event.Payload, &call) != nil || call.ToolCallID == "" {
		// Cline also publishes a hook's copy of each tool call, with no id. The
		// copy that carries the id is the one that counts.
		return
	}
	a.Mu.Lock()
	_, open := a.out.toolOwner[call.ToolCallID]
	dropped := a.out.dropped[call.ToolCallID]
	a.Mu.Unlock()
	if open || dropped {
		return
	}
	t := a.routeContent()
	if t == nil {
		a.Mu.Lock()
		a.out.dropped[call.ToolCallID] = true
		a.Mu.Unlock()
		return
	}
	a.handleToolStarted(t, event, call)
	if startsSubagent(call.ToolName) {
		a.openSpawn(t, event, call)
	}
}

// dispatchToolUpdated streams one call's output to the transcript that shows
// the call.
func (a *Agent) dispatchToolUpdated(event hubEvent) {
	var call toolEvent
	if json.Unmarshal(event.Payload, &call) != nil || call.ToolCallID == "" {
		return
	}
	a.Mu.Lock()
	t := a.out.toolOwner[call.ToolCallID]
	a.Mu.Unlock()
	if t != nil {
		a.handleToolUpdated(t, call)
	}
}

// dispatchToolFinished closes one call in the transcript that shows it.
func (a *Agent) dispatchToolFinished(event hubEvent) {
	var call toolEvent
	if json.Unmarshal(event.Payload, &call) != nil || call.ToolCallID == "" {
		return
	}
	a.Mu.Lock()
	t := a.out.toolOwner[call.ToolCallID]
	if t == nil {
		// A call that reached no transcript, or one that a turn end closed
		// already.
		delete(a.out.dropped, call.ToolCallID)
	}
	spawn := a.out.spawns[call.ToolCallID]
	a.Mu.Unlock()
	if t == nil {
		return
	}
	a.handleToolFinished(t, event, call)
	if spawn != nil {
		a.closeSpawn(spawn, call)
	}
}

// handleSessionNotice persists a notice of the lead: a compaction of its
// context, and each status that Cline shows the user. A notice of a subagent
// or a teammate is theirs, and Cline states no transcript that could show it
// in its place.
func (a *Agent) handleSessionNotice(event hubEvent) {
	var notice struct {
		Message string `json:"message"`
		Agent   struct {
			Kind string `json:"kind"`
		} `json:"agent"`
	}
	if json.Unmarshal(event.Payload, &notice) != nil || notice.Message == "" {
		return
	}
	if notice.Agent.Kind != "" && notice.Agent.Kind != agentKindLead {
		return
	}
	if a.IsDiscardingOutput() {
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.Raw); err != nil {
		slog.Error("cline persist notice", "agent_id", a.AgentID(), "error", err)
	}
}
