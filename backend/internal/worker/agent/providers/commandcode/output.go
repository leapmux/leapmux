package commandcode

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func (a *Agent) messageContent(raw []byte) agent.MessageContent {
	a.Mu.Lock()
	id := a.sessionID
	a.Mu.Unlock()
	return agent.MessageContent{Original: raw, AgentSessionID: id}
}

func (a *Agent) persist(raw []byte, span agent.SpanInfo) {
	a.flushPendingRunError()
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, a.messageContent(raw), span); err != nil {
		slog.Warn("persist a Command Code message", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) persistNotification(raw []byte) {
	a.flushPendingRunError()
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}); err != nil {
		slog.Error("persist a Command Code notification", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) flushPendingRunError() {
	record := a.pendingRunError
	a.pendingRunError = nil
	if record == nil {
		return
	}
	if _, err := record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT); err != nil {
		slog.Error("retain the Command Code run error", "agent_id", a.AgentID(), "error", err)
	}
}

// commandCodeWorkerNotifications keeps a held native error before a worker-written notice.
type commandCodeWorkerNotifications struct{ a *Agent }

func (sink commandCodeWorkerNotifications) PersistLeapMuxNotification(content map[string]interface{}) {
	sink.a.flushPendingRunError()
	sink.a.sink.PersistLeapMuxNotification(content)
}

// finishPendingRunError drops only a nonempty diagnostic that the native end repeats.
func (a *Agent) finishPendingRunError(raw []byte) {
	if a.pendingRunError == nil {
		return
	}
	var end struct {
		Params struct {
			Error json.RawMessage `json:"error"`
		} `json:"params"`
	}
	event, err := eventFromFrame(a.pendingRunError.FrozenContent().Original)
	if err == nil && json.Unmarshal(raw, &end) == nil {
		message := nativeErrorText(event.Error)
		if strings.TrimSpace(message) != "" && message == nativeErrorText(end.Params.Error) {
			a.pendingRunError = nil
			return
		}
	}
	a.flushPendingRunError()
}

func (a *Agent) handleMethod(line *providerkit.ParsedLine) {
	if line.HasID() {
		a.RefuseUnsupportedRequest(line)
		return
	}
	switch line.Method {
	case methodHello:
		// The handshake supplies no transcript content.
		return
	case methodProtocolError:
		a.persistNotification(line.Raw)
		return
	case methodSteersDelivered, methodTurnStarted, contracts.CommandCodeMethodTurnCompleted:
		// These methods carry the turn state below.
	default:
		a.persist(line.Raw, agent.SpanInfo{})
		return
	}
	var envelope struct {
		Params struct {
			TurnID     string `json:"turnId"`
			StopReason string `json:"stopReason"`
			Count      int    `json:"count"`
		} `json:"params"`
	}
	if json.Unmarshal(line.Raw, &envelope) != nil {
		return
	}
	switch line.Method {
	case methodSteersDelivered:
		count := envelope.Params.Count
		if count <= 0 {
			return
		}
		if len(a.steers.Pending()) == 0 {
			slog.Warn("Command Code reported a steer with no pending input", "agent_id", a.AgentID(), "count", count)
			return
		}
		a.flushPendingRunError()
		if err := providerkit.FlushGeneration(&a.generation, a.sink, agent.MessageCompletionComplete, a.IsDiscardingOutput()); err != nil {
			slog.Error("retain Command Code output before a steer", "agent_id", a.AgentID(), "error", err)
		}
		delivered := a.steers.EchoOldest(count)
		if delivered < count {
			slog.Warn("Command Code reported more steers than the Worker sent", "agent_id", a.AgentID(), "reported", count, "matched", delivered)
		}
		a.steers.Settle(a.sink)
	case methodTurnStarted:
		a.startTurn(envelope.Params.TurnID)
	case contracts.CommandCodeMethodTurnCompleted:
		a.completeTurn(line.Raw, envelope.Params.TurnID, envelope.Params.StopReason, nil)
	}
}

func (a *Agent) handleEvent(raw []byte) {
	var wrapper struct {
		Type  string          `json:"type"`
		Event json.RawMessage `json:"event"`
	}
	if json.Unmarshal(raw, &wrapper) != nil || wrapper.Type != contracts.CommandCodeFrameKindEvent {
		return
	}
	var event nativeEvent
	if json.Unmarshal(wrapper.Event, &event) != nil {
		// A new native shape must remain available for inspection.
		a.persist(raw, agent.SpanInfo{})
		return
	}
	switch event.Type {
	case contracts.CommandCodeEventStreamRestart:
		providerkit.DiscardGenerationScopes(&a.generation, a.sink, []string{"text", "thinking"})
	case eventTextDelta, eventThinkingDelta:
		if event.Delta == "" {
			return
		}
		a.retryResolution.Resolve(commandCodeWorkerNotifications{a: a})
		kind := agent.AssembledMessageKindText
		scope := "text"
		if event.Type == eventThinkingDelta {
			kind, scope = agent.AssembledMessageKindReasoning, "thinking"
		}
		a.generation.Append(scope, kind, event.Delta, providerkit.JoinVerbatim)
		a.sink.ReportProgress(agent.ModelTextProgress(scope, event.Delta))
	case contracts.CommandCodeEventThinkingEnd:
		if event.Text != "" {
			a.persist(raw, agent.SpanInfo{})
		}
		a.generation.Discard("thinking")
	case contracts.CommandCodeEventMessageEnd:
		if nativeText(event.Content) != "" {
			a.persist(raw, agent.SpanInfo{})
		}
		a.generation.Discard("text")
		a.generation.Discard("thinking")
	case contracts.CommandCodeEventToolQueued:
		if event.ToolCallID == "" || event.ToolName == "" {
			return
		}
		a.Mu.Lock()
		if _, exists := a.tools[event.ToolCallID]; exists {
			a.Mu.Unlock()
			return
		}
		a.tools[event.ToolCallID] = openTool{name: event.ToolName, input: slices.Clone(event.Input), opening: slices.Clone(raw)}
		a.TurnToolUses++
		a.Mu.Unlock()
		a.retryResolution.Resolve(commandCodeWorkerNotifications{a: a})
		a.persist(raw, agent.SpanInfo{SpanID: event.ToolCallID, SpanType: event.ToolName})
		a.sink.OpenSpan(event.ToolCallID, "")
		a.sink.SetSpanType(event.ToolCallID, event.ToolName)
	case contracts.CommandCodeEventToolUpdate:
		if event.ToolCallID == "" {
			return
		}
		a.Mu.Lock()
		_, known := a.tools[event.ToolCallID]
		a.Mu.Unlock()
		if !known {
			return
		}
		text := nativeText(event.Partial)
		a.sink.ReportProgress(agent.OutputDeltaProgress(event.ToolCallID, int64(len(text))))
		a.sink.ReportProgress(agent.OutputTailProgress(event.ToolCallID, text, false))
	case contracts.CommandCodeEventToolCompleted, contracts.CommandCodeEventToolErrored, contracts.CommandCodeEventToolDenied, contracts.CommandCodeEventToolHookBlocked:
		a.finishTool(raw, event)
	case contracts.CommandCodeEventModelRequestEnd:
		a.recordUsage(event.Usage)
	case contracts.CommandCodeEventCompactionStart:
		a.Mu.Lock()
		a.compacting = true
		a.compactionLive = true
		a.Mu.Unlock()
		trigger := event.Trigger
		if trigger == "" {
			trigger = "auto"
		}
		providerkit.PublishCompaction(a.sink, trigger)
		a.PublishTurnActive()
	case contracts.CommandCodeEventCompactionDone:
		a.Mu.Lock()
		if event.Trigger == contracts.CommandCodeCompactionTriggerManual && a.manualCompaction != nil {
			a.manualCompaction.finish()
			a.manualCompaction = nil
		}
		a.Mu.Unlock()
		a.endLiveCompaction()
		a.PublishTurnActive()
	case contracts.CommandCodeEventCompactionOutcome:
		a.persistNotification(raw)
		a.Mu.Lock()
		if event.Trigger == contracts.CommandCodeCompactionTriggerManual && a.manualCompaction != nil {
			a.manualCompaction.outcome = event.Outcome
		}
		a.Mu.Unlock()
		if !a.endLiveCompaction() {
			providerkit.EndCompaction(a.sink)
		}
		a.PublishTurnActive()
		return
	case contracts.CommandCodeEventRunError:
		a.flushPendingRunError()
		record := agent.CaptureTranscript(a.sink, a.messageContent(raw), agent.SpanInfo{})
		a.pendingRunError = &record
		return
	case contracts.CommandCodeEventNotice, contracts.CommandCodeEventApiRetry,
		contracts.CommandCodeEventSkillLoaded, contracts.CommandCodeEventModError,
		contracts.CommandCodeEventContinuationRecovery, contracts.CommandCodeEventToolHooks:
		// The shared membership table selects the notification write below.
	case contracts.CommandCodeEventSubagentStart, contracts.CommandCodeEventSubagentStop, contracts.CommandCodeEventSubagentProgress:
		a.observeChild(raw, event)
	case contracts.CommandCodeEventPermissionModeChanged:
		a.Mu.Lock()
		a.permissionMode = event.Mode
		a.Mu.Unlock()
		a.sink.UpdatePermissionMode(event.Mode)
	case contracts.CommandCodeEventConfigSettingChanged:
		// A later native state reply confirms the applied settings.
	case contracts.CommandCodeEventRunStart, contracts.CommandCodeEventRunEnd:
		// Native run_end repeats the whole session. turn/completed owns the boundary.
	case eventThinkingStart, eventMessageStart, eventMessageUpdate, eventTurnStart, eventTurnEnd,
		eventModelRequestStart, eventToolRunning, eventInterrupted, eventSessionTitled,
		eventModelTrace, eventToolInputCoerced, eventToolInputRepaired:
		// These events contain progress or native diagnostics. Completed events supply the transcript content.
	default:
		a.persist(raw, agent.SpanInfo{})
		return
	}
	if slices.Contains(contracts.CommandCodeNotificationEventMembers, event.Type) {
		a.persistNotification(raw)
		if event.Type == contracts.CommandCodeEventApiRetry || event.Type == contracts.CommandCodeEventStreamRestart {
			a.retryResolution.NoteRetry()
		}
	}
}

// endLiveCompaction clears the automatic-operation flag and withdraws a published start.
// The manual request keeps its separate guard until its own completion frame.
func (a *Agent) endLiveCompaction() bool {
	a.Mu.Lock()
	live := a.compactionLive
	a.compactionLive, a.compacting = false, false
	a.Mu.Unlock()
	if live {
		providerkit.EndCompaction(a.sink)
	}
	return live
}

func (a *Agent) endUnfinishedCompaction(reason error) {
	if a.endLiveCompaction() {
		outcome, detail := providerkit.CompactionEndAborted, ""
		if reason != nil {
			outcome, detail = providerkit.CompactionEndFailed, reason.Error()
		}
		providerkit.PersistCompactionEnded(commandCodeWorkerNotifications{a: a}, outcome, detail)
	}
}

func (a *Agent) finishTool(raw []byte, event nativeEvent) {
	if event.ToolCallID == "" {
		return
	}
	a.Mu.Lock()
	opening, known := a.tools[event.ToolCallID]
	delete(a.tools, event.ToolCallID)
	a.Mu.Unlock()
	if !known {
		return
	}
	a.persist(raw, agent.SpanInfo{SpanID: event.ToolCallID, SpanType: opening.name, Closing: true})
	a.sink.CloseSpan(event.ToolCallID)
	a.sink.ReportProgress(agent.CompleteOutputProgress(event.ToolCallID))
	a.finishChildTool(event, opening)
}

func (a *Agent) completeTurn(raw []byte, turnID, stopReason string, compactionFailure error) {
	if turnID == "" {
		return
	}
	a.Mu.Lock()
	if slices.Contains(a.finishedTurns, turnID) {
		a.Mu.Unlock()
		return
	}
	a.finishedTurns = append(a.finishedTurns, turnID)
	if len(a.finishedTurns) > 32 {
		a.finishedTurns = a.finishedTurns[len(a.finishedTurns)-32:]
	}
	active := a.turnID == turnID
	started, uses, usage := a.turnStarted, a.TurnToolUses, maps.Clone(a.usage)
	a.Mu.Unlock()
	if !active {
		return
	}
	a.finishPendingRunError(raw)
	a.retryResolution.Reset()
	a.steers.Reset()
	completion := agent.MessageCompletionComplete
	if stopReason == "interrupted" {
		completion = agent.MessageCompletionInterrupted
	}
	if stopReason == "run_error" {
		completion = agent.MessageCompletionError
	}
	if err := a.generation.PersistAllTo(completion, a.sink, agent.MessageContent{Completion: completion}, agent.SpanInfo{}); err != nil {
		slog.Warn("retain partial Command Code output", "agent_id", a.AgentID(), "error", err)
	}
	a.closeUnfinishedTools(completion)
	a.endUnfinishedCompaction(compactionFailure)
	duration := max(int64(0), a.clock.Now().Sub(started).Milliseconds())
	metadata, err := json.Marshal(map[string]any{contracts.MessageMetadataFieldDurationMs: duration, contracts.SessionInfoKeyContextUsage: usage})
	if err != nil {
		slog.Warn("encode Command Code turn metadata", "agent_id", a.AgentID(), "error", err)
	}
	content := a.messageContent(raw)
	content.Completion, content.Metadata = completion, metadata
	content = agent.WithToolUseCount(content, uses)
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Warn("persist the Command Code turn end", "agent_id", a.AgentID(), "error", err)
	}
	a.Mu.Lock()
	if a.turnID == turnID {
		a.turnID = ""
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

func (a *Agent) closeUnfinishedTools(completion agent.MessageCompletion) {
	a.Mu.Lock()
	tools := a.tools
	a.tools = make(map[string]openTool)
	a.Mu.Unlock()
	for id, tool := range tools {
		content := a.messageContent(tool.opening)
		content.Completion = completion
		if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{SpanID: id, SpanType: tool.name, Closing: true}); err != nil {
			slog.Warn("close an incomplete Command Code tool", "agent_id", a.AgentID(), "error", err)
		}
		a.sink.CloseSpan(id)
	}
}

func (a *Agent) finishStream() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.IsDiscardingOutput() {
		return
	}
	a.flushPendingRunError()
	a.Mu.Lock()
	turnID := a.turnID
	compacting := a.compacting
	a.compacting = false
	var compactionFailure error
	if a.manualCompaction != nil {
		compactionFailure = a.manualCompaction.failure
		a.manualCompaction.outcome = contracts.CommandCodeCompactionOutcomeFailed
		a.manualCompaction.finish()
		a.manualCompaction = nil
	}
	a.Mu.Unlock()
	if turnID != "" {
		stopReason := "run_error"
		if a.ProcessExitCompletion() == agent.MessageCompletionInterrupted {
			stopReason = "interrupted"
		}
		// A process exit needs a boundary even when the native host emitted none.
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": contracts.CommandCodeMethodTurnCompleted, "params": map[string]any{"turnId": turnID, "stopReason": stopReason, "error": map[string]string{"message": "The Command Code process exited before the turn completed."}}})
		if err == nil {
			a.completeTurn(raw, turnID, stopReason, compactionFailure)
		}
	} else {
		a.endUnfinishedCompaction(compactionFailure)
	}
	if a.sink != nil {
		if compacting && turnID == "" {
			a.PublishTurnActive()
		}
		a.closeChildren()
	}
}

func (a *Agent) recordUsage(usage *nativeUsage) {
	if usage == nil || usage.Input < 0 || usage.Output < 0 || usage.CacheRead < 0 || usage.CacheWrite < 0 {
		return
	}
	reading := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite})
	a.Mu.Lock()
	model := agent.FindAvailableModel(a.models, a.model)
	if model != nil && model.ContextWindow > 0 {
		reading[contracts.ContextUsageFieldContextWindow] = model.ContextWindow
	}
	a.usage = reading
	a.Mu.Unlock()
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: reading})
}
