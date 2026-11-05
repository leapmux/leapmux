package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) applyStep(state *cascadeState, index int, step antigravityStep, raw json.RawMessage) {
	a.Mu.Lock()
	record := state.steps[index]
	if record == nil {
		record = &stepState{}
		state.steps[index] = record
	}
	previousStatus := record.status
	record.status = step.Status
	record.raw = append(json.RawMessage(nil), raw...)
	record.modelUsage = step.Metadata.ModelUsage
	spanID := record.spanID
	if spanID == "" {
		spanID = step.Metadata.ToolCall.ID
		if spanID == "" {
			spanID = fmt.Sprintf("antigravity-step-%d", index)
		}
		record.spanID = spanID
	}
	_, background := state.background[index]
	record.background = background
	a.Mu.Unlock()

	if step.Type == contracts.AntigravityStepTypePlannerResponse {
		a.applyPlannerResponse(state, index, step)
	}
	if step.Type == contracts.AntigravityStepTypeInvokeSubagent {
		a.linkSubagentResults(state, index, step)
	}
	if step.Type == contracts.AntigravityStepTypeErrorMessage && !isOpenStepStatus(step.Status) {
		a.Mu.Lock()
		state.heldError = stepErrorText(step)
		state.retryPending = false
		a.Mu.Unlock()
	}
	if step.Status == contracts.AntigravityStepStatusWaiting {
		a.registerInteraction(state, index, step)
	} else if previousStatus == contracts.AntigravityStepStatusWaiting {
		a.withdrawInteraction(state, index)
	}
	if isOpenStepStatus(step.Status) {
		if isToolStep(step) {
			state.sink.OpenSpan(spanID, "")
			a.persistLiveStep(state, index, step, raw, spanID)
			if output := liveStepOutput(step); output != "" {
				state.sink.ReportProgress(agent.OutputTailProgress(spanID, output, false))
			}
		}
		return
	}
	a.persistStep(state, index, step, raw)
}

func (a *Agent) persistLiveStep(state *cascadeState, index int, step antigravityStep, raw json.RawMessage, spanID string) {
	a.Mu.Lock()
	record := state.steps[index]
	if record == nil || record.livePersisted {
		a.Mu.Unlock()
		return
	}
	record.livePersisted = true
	a.Mu.Unlock()
	cleaned, err := scrubStep(raw)
	if err != nil {
		a.Mu.Lock()
		record.livePersisted = false
		a.Mu.Unlock()
		slog.Error("antigravity scrub live step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
		return
	}
	err = state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: cleaned, AgentSessionID: state.id, IdempotencyKey: fmt.Sprintf("antigravity:%s:%d:live", state.id, index),
	}, agent.SpanInfo{SpanID: spanID, SpanType: contracts.AntigravityFrameStep})
	if err != nil {
		a.Mu.Lock()
		record.livePersisted = false
		a.Mu.Unlock()
		slog.Error("antigravity persist live step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
	}
}

func isToolStep(step antigravityStep) bool {
	switch step.Type {
	case contracts.AntigravityStepTypePlannerResponse, contracts.AntigravityStepTypeUserInput,
		contracts.AntigravityStepTypeSystemMessage, contracts.AntigravityStepTypeErrorMessage,
		contracts.AntigravityStepTypeCheckpoint:
		return false
	default:
		return true
	}
}

func liveStepOutput(step antigravityStep) string {
	if step.RunCommand.CombinedOutput.Full != "" {
		return step.RunCommand.CombinedOutput.Full
	}
	return ""
}

func (a *Agent) applyPlannerResponse(state *cascadeState, index int, step antigravityStep) {
	text := step.PlannerResponse.ModifiedResponse
	if text == "" {
		text = step.PlannerResponse.Response
	}
	scope := fmt.Sprintf("antigravity:%s:%d", state.id, index)
	a.Mu.Lock()
	record := state.steps[index]
	previous := record.text
	record.text = text
	record.thinking = step.PlannerResponse.Thinking
	retryPending := state.retryPending
	state.retryPending = false
	state.heldError = ""
	a.Mu.Unlock()
	if text != previous {
		delta := text
		if strings.HasPrefix(text, previous) {
			delta = text[len(previous):]
		} else {
			state.sink.ReportProgress(agent.ResetModelScopeProgress(scope))
		}
		if delta != "" {
			state.sink.ReportProgress(agent.ModelTextProgress(scope, delta))
		}
	}
	if text != "" || step.PlannerResponse.Thinking != "" {
		if retryPending {
			state.sink.PersistLeapMuxNotification(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved})
		}
	}
}

func (a *Agent) persistStep(state *cascadeState, index int, step antigravityStep, raw json.RawMessage) {
	a.Mu.Lock()
	record := state.steps[index]
	if record != nil && record.persisted {
		a.Mu.Unlock()
		return
	}
	if record != nil {
		record.persisted = true
	}
	interrupted := state.interrupted
	spanID := ""
	if record != nil && isToolStep(step) {
		spanID = record.spanID
	}
	a.Mu.Unlock()

	switch step.Type {
	case contracts.AntigravityStepTypeUserInput:
		if state.parent == nil {
			return
		}
	case contracts.AntigravityStepTypeSystemMessage, contracts.AntigravityStepTypeErrorMessage:
		return
	}
	cleaned, err := scrubStep(raw)
	if err != nil {
		a.Mu.Lock()
		record.persisted = false
		a.Mu.Unlock()
		slog.Error("antigravity scrub step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
		return
	}
	span := agent.SpanInfo{}
	if spanID != "" {
		span = agent.SpanInfo{SpanID: spanID, SpanType: contracts.AntigravityFrameStep, Closing: true}
	}
	completion := agent.MessageCompletionComplete
	if interrupted {
		completion = agent.MessageCompletionInterrupted
	} else if step.Status == contracts.AntigravityStepStatusCanceled {
		completion = agent.MessageCompletionInterrupted
	} else if step.Status == contracts.AntigravityStepStatusError && !stepDeclined(step) {
		completion = agent.MessageCompletionError
	}
	content := agent.MessageContent{
		Original: cleaned, Completion: completion, AgentSessionID: state.id,
		IdempotencyKey: fmt.Sprintf("antigravity:%s:%d", state.id, index),
	}
	contents := []agent.MessageContent{content}
	text := step.PlannerResponse.ModifiedResponse
	if text == "" {
		text = step.PlannerResponse.Response
	}
	if step.Type == contracts.AntigravityStepTypePlannerResponse && text != "" && step.PlannerResponse.Thinking != "" {
		thinkingRaw, splitErr := plannerThinkingOnly(cleaned)
		if splitErr != nil {
			slog.Error("antigravity split planner thinking", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", splitErr)
			return
		}
		thinking := content
		thinking.Original = thinkingRaw
		thinking.IdempotencyKey += ":thinking"
		contents = []agent.MessageContent{thinking, content}
	}
	for _, item := range contents {
		if err := state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, item, span); err != nil {
			a.Mu.Lock()
			if current := state.steps[index]; current != nil {
				current.persisted = false
			}
			a.Mu.Unlock()
			slog.Error("antigravity persist step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
			return
		}
	}
	if spanID != "" {
		state.sink.CloseSpan(spanID)
		state.sink.ReportProgress(agent.CompleteOutputProgress(spanID))
	}
	if step.Type == contracts.AntigravityStepTypePlannerResponse {
		state.sink.ReportProgress(agent.CompleteModelProgress(fmt.Sprintf("antigravity:%s:%d", state.id, index)))
	}
	if step.Type == contracts.AntigravityStepTypeInvokeSubagent {
		a.linkSubagentResults(state, index, step)
	}
	if background, ok := a.backgroundForStep(state, index); ok {
		a.finishBackgroundStep(state, background, step)
	}
	if state.parent == nil && step.Status == contracts.AntigravityStepStatusDone {
		a.recordPlanStep(state, step)
	}
}

func plannerThinkingOnly(raw json.RawMessage) (json.RawMessage, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	planner, ok := value["plannerResponse"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("planner response is not an object")
	}
	planner["response"] = ""
	planner["modifiedResponse"] = ""
	return json.Marshal(value)
}

func (a *Agent) persistUnknownStep(state *cascadeState, index int, raw json.RawMessage) {
	a.Mu.Lock()
	record := state.steps[index]
	if record != nil && record.persisted {
		a.Mu.Unlock()
		return
	}
	if record == nil {
		record = &stepState{}
		state.steps[index] = record
	}
	record.persisted = true
	a.Mu.Unlock()
	if err := state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: append(json.RawMessage(nil), raw...), AgentSessionID: state.id,
		IdempotencyKey: fmt.Sprintf("antigravity:%s:%d", state.id, index),
	}, agent.SpanInfo{}); err != nil {
		a.Mu.Lock()
		record.persisted = false
		a.Mu.Unlock()
		slog.Error("antigravity persist unknown step", "agent_id", a.AgentID(), "error", err)
	}
}

func scrubStep(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	scrubOpaqueFields(value)
	return json.Marshal(value)
}

func scrubOpaqueFields(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "thinkingSignature")
		if metadata, ok := typed["metadata"].(map[string]any); ok {
			delete(metadata, "internalMetadata")
		}
		for _, child := range typed {
			scrubOpaqueFields(child)
		}
	case []any:
		for _, child := range typed {
			scrubOpaqueFields(child)
		}
	}
}

func (a *Agent) noteModelRetry(state *cascadeState, request *modelRequestStatus) {
	a.Mu.Lock()
	if state.retryPending {
		a.Mu.Unlock()
		return
	}
	state.retryPending = true
	state.heldError = ""
	a.Mu.Unlock()
	raw, err := json.Marshal(map[string]any{"name": contracts.AntigravityFrameInteraction, "modelRequest": request})
	if err != nil {
		return
	}
	if _, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, AgentSessionID: state.id}); err != nil {
		slog.Error("antigravity persist model retry", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) publishContextUsage(state *cascadeState, usage *contextWindowUsage) {
	a.Mu.Lock()
	state.contextWindow = usage.MaxContextTokens
	state.contextTokens = usage.EstimatedTokensUsed
	a.Mu.Unlock()
	values := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{})
	values[contracts.ContextUsageFieldContextWindow] = usage.MaxContextTokens
	values[contracts.ContextUsageFieldContextTokens] = usage.EstimatedTokensUsed
	state.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: values})
}

func (a *Agent) endTurn(state *cascadeState, completion agent.MessageCompletion) {
	a.Mu.Lock()
	if !state.turnActive {
		a.Mu.Unlock()
		return
	}
	if state.interrupted {
		completion = agent.MessageCompletionInterrupted
	} else if state.heldError != "" {
		completion = agent.MessageCompletionError
	}
	heldError := state.heldError
	seq := a.NextTurnSeq()
	a.Mu.Unlock()

	a.flushOpenSteps(state, completion)
	if state.parent == nil && completion == agent.MessageCompletionComplete {
		a.maybePublishPlan(state)
	}
	if heldError != "" {
		raw, _ := json.Marshal(map[string]any{"name": contracts.AntigravityFrameStep, "type": contracts.AntigravityStepTypeErrorMessage, "errorMessage": map[string]any{"error": map[string]any{"userErrorMessage": heldError}}})
		if _, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, Completion: agent.MessageCompletionError, AgentSessionID: state.id}); err != nil {
			slog.Error("antigravity persist agent error", "agent_id", a.AgentID(), "error", err)
		}
	}
	if state.parent != nil {
		status := bgtask.StatusSucceeded
		if state.stopped || completion == agent.MessageCompletionInterrupted {
			status = bgtask.StatusStopped
		} else if completion == agent.MessageCompletionError {
			status = bgtask.StatusFailed
		}
		a.closeChildRow(state, status)
	} else {
		frame := a.turnEndFrame(state, completion)
		if err := state.sink.PersistTurnEnd(frame, agent.SpanInfo{}); err != nil {
			slog.Error("antigravity persist turn end", "agent_id", a.AgentID(), "error", err)
		}
		a.refreshRateLimits()
	}
	a.Mu.Lock()
	state.turnActive = false
	state.sendInFlight = false
	state.heldError = ""
	state.retryPending = false
	backgroundOpen := len(state.background) > 0
	a.Mu.Unlock()
	if !backgroundOpen {
		state.sink.ResetSpans()
	}
	state.sink.ReportProgress(agent.ResetProgress())
	if state.parent != nil {
		providerkit.PublishSteerableTurnActiveTo(state.sink, false, seq)
	} else {
		providerkit.PublishTurnStateTo(a.sink, agent.TurnState{}, seq)
	}
}

func (a *Agent) flushOpenSteps(state *cascadeState, completion agent.MessageCompletion) {
	a.Mu.Lock()
	type pending struct {
		index int
		step  antigravityStep
		raw   json.RawMessage
	}
	var pendingSteps []pending
	for index, record := range state.steps {
		if record.persisted || !isOpenStepStatus(record.status) {
			continue
		}
		if _, background := state.background[index]; background {
			continue
		}
		var step antigravityStep
		if err := json.Unmarshal(record.raw, &step); err != nil {
			continue
		}
		if completion != agent.MessageCompletionComplete {
			step.Status = "CORTEX_STEP_STATUS_CANCELED"
		}
		pendingSteps = append(pendingSteps, pending{index: index, step: step, raw: record.raw})
	}
	a.Mu.Unlock()
	for _, item := range pendingSteps {
		a.persistStep(state, item.index, item.step, item.raw)
	}
}

func (a *Agent) turnEndFrame(state *cascadeState, completion agent.MessageCompletion) agent.MessageContent {
	a.Mu.Lock()
	uses := 0
	usage := map[string]int64{}
	for index, record := range state.steps {
		if _, current := state.turnSteps[index]; !current {
			continue
		}
		var step antigravityStep
		if json.Unmarshal(record.raw, &step) == nil && isToolStep(step) && record.persisted {
			uses++
		}
		for key, value := range record.modelUsage {
			number, err := strconv.ParseInt(value, 10, 64)
			if err == nil {
				usage[key] += number
			}
		}
	}
	contextWindow, contextTokens := state.contextWindow, state.contextTokens
	a.Mu.Unlock()
	fields := map[string]any{"name": contracts.AntigravityFrameTurnEnd, "num_tool_uses": uses}
	if len(usage) > 0 {
		fields["metadata"] = map[string]any{"modelUsage": usage}
	}
	values := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
		Input: usage["inputTokens"], CacheWrite: usage["cacheWriteTokens"], CacheRead: usage["cacheReadTokens"], Output: usage["outputTokens"],
	})
	if contextWindow > 0 {
		values[contracts.ContextUsageFieldContextWindow] = contextWindow
	}
	if contextTokens > 0 {
		values[contracts.ContextUsageFieldContextTokens] = contextTokens
	}
	state.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: values})
	raw, _ := json.Marshal(fields)
	return agent.MessageContent{Original: raw, Completion: completion, AgentSessionID: state.id}
}

func (a *Agent) fatalCascade(state *cascadeState, streamErr error) {
	if streamErr == nil {
		streamErr = fmt.Errorf("antigravity stream ended")
	}
	a.Mu.Lock()
	if state.closed {
		a.Mu.Unlock()
		return
	}
	state.closed = true
	state.connectErr = streamErr
	state.sendInFlight = false
	rootBeforeStart := state.parent == nil && !state.seenUpdate
	active := state.turnActive
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	state.connectedOnce.Do(func() { close(state.connected) })
	if rootBeforeStart {
		return
	}
	text := fmt.Sprintf("Antigravity CLI %s lost the language-server stream: %s", a.version, streamErrorText(streamErr))
	if state.parent == nil {
		state.sink.PersistLeapMuxNotification(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldText: text})
		a.endTurn(state, agent.MessageCompletionError)
		go a.Stop()
		return
	}
	if active || state.rowOpen || len(state.controls) > 0 || len(state.background) > 0 {
		state.sink.PersistLeapMuxNotification(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldText: text})
		a.endTurn(state, agent.MessageCompletionError)
		a.retireAllInteractions(state)
		a.closeChildRow(state, bgtask.StatusFailed)
		a.closeBackgroundTasks(state, bgtask.StatusEndedWithUnknownOutcome)
		providerkit.PublishSteerableTurnActiveTo(state.sink, false, seq)
	}
}

func (a *Agent) closeChildRow(state *cascadeState, status bgtask.Status) {
	a.Mu.Lock()
	if !state.rowOpen {
		a.Mu.Unlock()
		return
	}
	state.rowOpen = false
	a.Mu.Unlock()
	providerkit.LogRegistryRefusal("antigravity", "close", a.sink.CloseBackgroundTask(state.id, status))
}
