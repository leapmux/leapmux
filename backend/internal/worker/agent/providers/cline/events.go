package cline

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The hub dispatcher sends events in native order. HandleOutput supplies the same path for tests.
// dispatchMu serializes each event with lifecycle changes.
// Text and tool events carry no agent identity. The worker uses the active spawn chain to find their transcript.
// Teammate notices carry their agent identity. handleSessionNotice resolves their active run.

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
	case eventIterationFinished:
		if t := a.routeWithinTurn(); t != nil {
			a.discardUnflushedReasoning(t)
		}
	case eventPendingPrompts:
		a.handlePendingPrompts(event)
	case eventPendingPromptSubmitted:
		a.handlePendingPromptSubmitted(event)
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
	case eventRunHeartbeat, eventAgentDone:
	// Heartbeats keep the connection active. The run end supplies the completion.
	case eventUINotify:
	// The hub's desktop notice repeats the run end.
	case eventCloud:
		// The cloud viewer consumes these replication snapshots and updates.
		// This provider drives local sessions, whose output uses the cases above.
	case eventSessionAttached, eventSessionDetached:
	// These events describe the hub's client registry.
	case eventSessionCreated, eventSessionDeleted:
	// The hub's session registry: a created or deleted session is bookkeeping.
	// A mode rebuild creates a session per change, and none of it is
	// conversation.
	default:
		if event.SessionID != "" && event.SessionID == a.currentSession() {
			a.Mu.Lock()
			target := a.out.lead
			a.Mu.Unlock()
			a.persistRow(target, event.Raw, noCompletion)
		} else {
			slog.Debug("cline ignored an unknown event outside the current session", "agent_id", a.AgentID(), "event", event.Event)
		}
	}
}

// handleRunStarted confirms the send that started the run.
// Cline sends another run.started for each steer. An active turn keeps its identity.
// A run that another client starts creates a turn that takes no steer.
func (a *Agent) handleRunStarted(event hubEvent) {
	var started *struct {
		RequestID string `json:"requestId"`
		ClientID  string `json:"clientId"`
	}
	if err := json.Unmarshal(event.Payload, &started); err != nil || started == nil {
		slog.Debug("cline ignored a malformed run start", "agent_id", a.AgentID(), "error", err)
		return
	}
	delivered := started.RequestID != "" && a.settleDelivery(started.RequestID, nil)
	a.Mu.Lock()
	// A send that stopped waiting already started this turn.
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

// handleSessionNotice routes a native notice without starting a turn.
func (a *Agent) handleSessionNotice(event hubEvent) {
	var notice struct {
		Message  string `json:"message"`
		Metadata struct {
			Kind  string `json:"kind"`
			Phase string `json:"phase"`
		} `json:"metadata"`
		Agent struct {
			Kind        string `json:"kind"`
			TeamAgentID string `json:"teamAgentId"`
		} `json:"agent"`
	}
	if json.Unmarshal(event.Payload, &notice) != nil || notice.Message == "" || a.IsDiscardingOutput() {
		return
	}
	var sink agent.ProviderServices
	var target *transcript
	switch notice.Agent.Kind {
	case "", agentKindLead:
		target = a.routeWithinTurn()
		if target != nil {
			sink = a.sinkFor(target)
		}
	case agentKindTeammate:
		if run := a.teamRunOfAgent(notice.Agent.TeamAgentID); run != nil && run.childID != "" {
			target = run.transcript
			sink = a.sinkFor(target)
		}
	}
	if sink == nil {
		slog.Debug("cline ignored a notice without an unambiguous owner", "agent_id", a.AgentID(), "kind", notice.Agent.Kind)
		return
	}
	if isCompactionNotice(notice.Metadata.Kind) {
		switch notice.Metadata.Phase {
		case contracts.ClineNoticePhaseStarted:
			a.Mu.Lock()
			target.compactionLive = true
			a.Mu.Unlock()
			providerkit.PublishCompaction(sink, "")
			return
		case contracts.ClineNoticePhaseCompleted, contracts.ClineNoticePhaseSkipped, contracts.ClineNoticePhaseFailed:
			a.Mu.Lock()
			target.compactionLive = false
			a.Mu.Unlock()
			providerkit.EndCompaction(sink)
		}
	}
	if target != nil && notice.Metadata.Kind == contracts.ClineNoticeKindMaxTokensCompaction && notice.Metadata.Phase == contracts.ClineNoticePhaseStarted {
		a.discardUnflushedReasoning(target)
	}
	if _, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.Raw}); !agent.OutputRetained(err) {
		slog.Error("cline persist notice", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	lead := target == a.out.lead
	a.Mu.Unlock()
	if lead && notice.Metadata.Kind == contracts.ClineNoticeKindProviderErrorRetry {
		if notice.Metadata.Phase == contracts.ClineNoticePhaseFailed {
			a.leadRetry.Reset()
		} else {
			a.leadRetry.NoteRetry()
		}
	}
}

// handlePendingPrompts replaces the steer count with the native queue snapshot.
func (a *Agent) handlePendingPrompts(event hubEvent) {
	var payload struct {
		Prompts []struct {
			Delivery string `json:"delivery"`
		} `json:"prompts"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil {
		return
	}
	count := 0
	for _, prompt := range payload.Prompts {
		if prompt.Delivery == deliverySteer {
			count++
		}
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if !a.turn.active || event.SessionID != a.sessionID {
		return
	}
	a.turn.steersPending = count
	a.turn.steerReports++
}

// handlePendingPromptSubmitted removes one steer after the native model reads it.
func (a *Agent) handlePendingPromptSubmitted(event hubEvent) {
	var payload struct {
		Prompt struct {
			Delivery string `json:"delivery"`
		} `json:"prompt"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || payload.Prompt.Delivery != deliverySteer {
		return
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if !a.turn.active || event.SessionID != a.sessionID {
		return
	}
	if a.turn.steersPending > 0 {
		a.turn.steersPending--
	}
	a.turn.steerReports++
}
