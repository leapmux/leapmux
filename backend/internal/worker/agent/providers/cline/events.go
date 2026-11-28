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
// Tagged events carry agent metadata and route directly to the lead or bound child transcript.
// Untagged text and tool events use the active spawn chain to find their transcript.
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
	decoded := decodeSteerEvent(event)
	ownSteerStart := a.observeSteerEvent(event, decoded)
	switch event.Event {
	case eventRunStarted:
		a.handleRunStarted(event, decoded.runStarted, ownSteerStart)
	case eventSessionUpdated:
		a.handleSessionUpdated(event)
	case eventIterationStarted:
		a.handleIterationStarted(event.Payload)
	case eventIterationFinished:
		if t := a.routeWithinTurn(event.Payload); t != nil {
			a.discardUnflushedReasoning(t)
		}
	case eventPendingPrompts:
		a.handlePendingPrompts(event, decoded.prompts)
	case eventPendingPromptSubmitted:
		a.handlePendingPromptSubmitted(event, decoded.submitted)
	case eventReasoningDelta:
		a.handleReasoningDelta(event.Payload)
	case eventAssistantDelta:
		a.handleAssistantDelta(event.Payload)
	case contracts.ClineEventAssistantFinished:
		a.handleAssistantFinished(event)
	case contracts.ClineEventReasoningFinished:
		a.handleReasoningFinished(event)
	case contracts.ClineEventAssistantMedia:
		if t := a.routeContent(event.Payload); t != nil {
			a.flushReasoning(t, event.SessionID)
			a.persistReplyRow(t, event.Raw, noCompletion)
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
	case eventAgentDone:
		a.noteLeadDone(event)
	case eventRunHeartbeat:
	// Heartbeats keep the connection active.
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
func (a *Agent) handleRunStarted(event hubEvent, started *runStartedPayload, ownSteer bool) {
	if started == nil {
		slog.Debug("cline ignored a malformed run start", "agent_id", a.AgentID())
		return
	}
	a.Mu.Lock()
	// A send that stopped waiting already started this turn.
	ours := started.RequestID != "" && a.turn.active && a.turn.requestID == started.RequestID
	retirePrevious := !ownSteer && a.detachedReply != nil && a.detachedReply.end == nil
	if reply := a.turn.inputReply; reply != nil && reply.receipt.SessionID == event.SessionID {
		if ours && started.ClientID == a.clientID {
			reply.started = true
		} else if !ownSteer {
			// Cline can read a steer in a later run. Another request makes
			// the session's unkeyed output ambiguous, even from this client.
			reply.ambiguous = true
		}
	}
	a.Mu.Unlock()
	if retirePrevious {
		a.retireDetachedReply()
	}
	delivered := started.RequestID != "" && a.settleDelivery(started.RequestID, nil)
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
//   - `idle`: a turn without a run ends immediately. The native queue's
//     autonomous run has no command result, so the idle state ends it too.
//     A run started by a command waits for its own end event and result.
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
		a.Mu.Lock()
		if reply := a.turn.inputReply; reply != nil && reply.receipt.SessionID == event.SessionID {
			if reply.autonomous {
				reply.started = true
			} else if a.turn.leadIdle {
				// No tracked submission explains the next run.
				reply.ambiguous = true
				a.turn.leadIdle = false
			}
		}
		a.Mu.Unlock()
		a.markRunStarted()
	case sessionStatusIdle:
		a.handleSessionIdle(event.SessionID)
	}
}

func (a *Agent) handleSessionIdle(sessionID string) {
	a.Mu.Lock()
	turn := a.turn
	reply := turn.inputReply
	if reply == nil || reply.receipt.SessionID != sessionID || !reply.started {
		a.Mu.Unlock()
		a.endRunlessTurnLocked(agent.MessageCompletionComplete, runReasonCompleted)
		return
	}
	reason := turn.leadDoneReason
	if !reply.autonomous {
		a.turn.leadIdle = reply.agentDone && reason != ""
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	if reply.ownsOutput(sessionID) {
		switch reason {
		case contracts.ClineRunReasonCompleted, contracts.ClineRunReasonMaxIterations:
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
		case contracts.ClineRunReasonAborted:
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
		case contracts.ClineRunReasonMistakeLimit, contracts.ClineRunReasonError:
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		}
	}
	if reason == "" {
		reason = runReasonCompleted
	}
	a.endTurn(completionOf(reason, turn.interruptRequested), runEndRow(sessionID, reason, ""), endTurnOptions{outcome: outcome})
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
	t := a.routeContentMeta(call.Agent)
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
		Agent eventAgent `json:"agent"`
	}
	if json.Unmarshal(event.Payload, &notice) != nil || notice.Message == "" || a.IsDiscardingOutput() {
		return
	}
	var sink agent.ProviderServices
	var target *transcript
	switch {
	case notice.Agent.Kind == agentKindTeammate:
		teamAgentID := notice.Agent.TeamAgentID
		if teamAgentID == "" {
			teamAgentID = notice.Agent.AgentID
		}
		if run := a.teamRunOfAgent(teamAgentID); run != nil && run.childID != "" {
			target = run.transcript
			sink = a.sinkFor(target)
		}
	case notice.Agent.hasTag():
		a.Mu.Lock()
		target, _ = a.routeTaggedLocked(notice.Agent)
		a.Mu.Unlock()
		if target != nil {
			sink = a.sinkFor(target)
		}
	case notice.Agent.Kind == "" || notice.Agent.Kind == agentKindLead:
		target = a.routeWithinTurn(nil)
		if target != nil {
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
func (a *Agent) handlePendingPrompts(event hubEvent, payload *pendingPromptsPayload) {
	if payload == nil {
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
func (a *Agent) handlePendingPromptSubmitted(event hubEvent, payload *pendingPromptSubmittedPayload) {
	if payload == nil || payload.Prompt.Delivery != deliverySteer {
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
