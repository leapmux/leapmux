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
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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
		a.deliverCommandCodeSteers(envelope.Params.TurnID, envelope.Params.Count)
	case methodTurnStarted:
		a.observeCommandCodeReplyStart(envelope.Params.TurnID)
		a.startTurn(envelope.Params.TurnID)
	case contracts.CommandCodeMethodTurnCompleted:
		a.completeTurnWithTeardown(line.Raw, envelope.Params.TurnID, envelope.Params.StopReason, nil, false)
	}
}

func (a *Agent) handleEvent(raw []byte) {
	var wrapper struct {
		Type  string          `json:"type"`
		Seq   json.RawMessage `json:"seq"`
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
			var seq uint64
			_ = json.Unmarshal(wrapper.Seq, &seq)
			a.persistCommandCodeReplyText(raw, seq)
		}
		a.generation.Discard("text")
		a.generation.Discard("thinking")
	case contracts.CommandCodeEventToolQueued:
		if event.ToolCallID == "" || event.ToolName == "" {
			return
		}
		a.Mu.Lock()
		if _, exists := a.tools.Get(event.ToolCallID); exists {
			a.Mu.Unlock()
			return
		}
		a.tools.Open(event.ToolCallID, event.ToolName, false, openTool{name: event.ToolName, input: slices.Clone(event.Input), opening: slices.Clone(raw)})
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
		_, known := a.tools.Get(event.ToolCallID)
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
	case contracts.CommandCodeEventSubagentStart, contracts.CommandCodeEventSubagentStop, contracts.CommandCodeEventSubagentProgress, eventSubagentBackgrounded:
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
	if live && a.sink != nil {
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
	entry, known, _ := a.tools.CompleteNative(event.ToolCallID)
	a.Mu.Unlock()
	if !known {
		return
	}
	opening := entry.Data
	a.persist(raw, agent.SpanInfo{SpanID: event.ToolCallID, SpanType: opening.name, Closing: true})
	a.sink.CloseSpan(event.ToolCallID)
	a.sink.ReportProgress(agent.CompleteOutputProgress(event.ToolCallID))
	a.finishChildTool(event, opening)
}

func (a *Agent) completeTurnWithTeardown(raw []byte, turnID, stopReason string, compactionFailure error, teardown bool) {
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
		if !teardown {
			a.finishCommandCodeNativeReply(turnID, stopReason)
		}
		return
	}
	a.finishPendingRunError(raw)
	a.retryResolution.Reset()
	if teardown {
		a.markPendingCommandCodeSteersUncertain()
	} else {
		a.pendingSteers = nil
	}
	completion := agent.MessageCompletionComplete
	if stopReason == "interrupted" {
		completion = agent.MessageCompletionInterrupted
	}
	if stopReason == "run_error" {
		completion = agent.MessageCompletionError
	}
	if err := a.persistCommandCodeGeneration(completion, false); err != nil {
		slog.Warn("retain partial Command Code output", "agent_id", a.AgentID(), "error", err)
	}
	closeTools := completion == agent.MessageCompletionInterrupted || teardown
	if closeTools {
		a.closeUnfinishedTools(completion)
	}
	childCloseStatus := bgtask.StatusEndedWithUnknownOutcome
	switch completion {
	case agent.MessageCompletionInterrupted:
		childCloseStatus = bgtask.StatusStopped
	case agent.MessageCompletionError:
		childCloseStatus = bgtask.StatusFailed
	case agent.MessageCompletionComplete, agent.MessageCompletionFinished:
		childCloseStatus = bgtask.StatusEndedWithUnknownOutcome
	}
	a.closeChildrenMatching(childCloseStatus, func(child *childState) bool { return !child.background })
	a.endUnfinishedCompaction(compactionFailure)
	metadataFields := map[string]any{contracts.SessionInfoKeyContextUsage: usage}
	if duration := providerkit.TurnDurationMillis(started, a.clock.Now()); duration != nil {
		metadataFields[contracts.MessageMetadataFieldDurationMs] = *duration
	}
	metadata, err := json.Marshal(metadataFields)
	if err != nil {
		slog.Warn("encode Command Code turn metadata", "agent_id", a.AgentID(), "error", err)
	}
	content := a.messageContent(raw)
	content.Completion, content.Metadata = completion, metadata
	content = agent.WithToolUseCount(content, uses)
	if err := a.sink.PersistTurnEnd(content, agent.SpanInfo{}); err != nil {
		slog.Warn("persist the Command Code turn end", "agent_id", a.AgentID(), "error", err)
		if !agent.OutputRetained(err) {
			a.replies.MarkUnproved(turnID)
		}
	}
	if closeTools {
		providerkit.ResetSpansAndProgress(a.sink)
	}
	if teardown {
		a.retireCommandCodeRepliesAfterExit()
	} else {
		a.finishCommandCodeNativeReply(turnID, stopReason)
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
	entries := a.tools.DrainAll()
	a.Mu.Unlock()
	if len(entries) == 0 || a.sink == nil {
		return
	}
	discarding := a.IsDiscardingOutput()
	providerkit.CloseToolSpans(a.sink, entries, completion, func(e providerkit.OpenToolEntry[openTool]) (agent.MessageContent, agent.SpanInfo, bool) {
		if discarding {
			return agent.MessageContent{}, agent.SpanInfo{SpanID: e.SpanID, SpanType: e.Data.name, Closing: true}, false
		}
		content := a.messageContent(e.Data.opening)
		content.Completion = completion
		return content, agent.SpanInfo{SpanID: e.SpanID, SpanType: e.Data.name, Closing: true}, true
	})
}

func (a *Agent) finishStream() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.replies.CloseStream()
	completion := a.ProcessExitCompletion()
	discarding := a.IsDiscardingOutput()
	a.Mu.Lock()
	turnID := a.turnID
	wasCompacting := a.compacting || a.manualCompaction != nil
	a.compacting = false
	var compactionFailure error
	if a.manualCompaction != nil {
		compactionFailure = a.manualCompaction.failure
		a.manualCompaction.outcome = contracts.CommandCodeCompactionOutcomeFailed
		a.manualCompaction.finish()
		a.manualCompaction = nil
	}
	if discarding {
		a.turnID = ""
	}
	a.Mu.Unlock()
	if discarding {
		a.pendingRunError = nil
		a.retryResolution.Reset()
		a.pendingSteers = nil
		a.endLiveCompaction()
		if a.sink != nil {
			_ = providerkit.FlushGeneration(&a.generation, a.sink, completion, true)
		} else {
			for _, scopeID := range a.generation.ScopeIDs() {
				a.generation.Discard(scopeID)
			}
		}
		a.closeUnfinishedTools(completion)
		if a.sink != nil {
			a.closeChildrenMatching(agent.IncompleteTaskStatus(completion), nil)
			providerkit.ResetSpansAndProgress(a.sink)
		}
		a.retireCommandCodeRepliesAfterExit()
		if a.sink != nil && (turnID != "" || wasCompacting) {
			a.PublishTurnActive()
		}
		return
	}
	a.flushPendingRunError()
	if turnID != "" {
		stopReason := "run_error"
		if completion == agent.MessageCompletionInterrupted {
			stopReason = "interrupted"
		}
		// A process exit needs a boundary even when the native host emitted none.
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": contracts.CommandCodeMethodTurnCompleted, "params": map[string]any{"turnId": turnID, "stopReason": stopReason, "error": map[string]string{"message": "The Command Code process exited before the turn completed."}}})
		if err == nil {
			a.completeTurnWithTeardown(raw, turnID, stopReason, compactionFailure, true)
		}
		a.retireCommandCodeRepliesAfterExit()
	} else {
		a.markPendingCommandCodeSteersUncertain()
		a.endUnfinishedCompaction(compactionFailure)
		a.closeUnfinishedTools(completion)
		if a.sink != nil {
			providerkit.ResetSpansAndProgress(a.sink)
		}
		a.retireCommandCodeRepliesAfterExit()
		if a.sink != nil && wasCompacting {
			a.PublishTurnActive()
		}
	}
	if a.sink != nil {
		a.closeChildrenMatching(agent.IncompleteTaskStatus(completion), nil)
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
