package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) applyStep(state *cascadeState, index int, step antigravityStep, raw json.RawMessage) error {
	if a.IsDiscardingOutput() {
		return nil
	}
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return nil
	}
	record := state.steps[index]
	if record == nil {
		record = &stepState{}
		state.steps[index] = record
	}
	if record.consumed {
		record.modelUsage = step.Metadata.ModelUsage
		a.Mu.Unlock()
		return nil
	}
	previousStatus := record.status
	record.status = step.Status
	record.raw = append(json.RawMessage(nil), raw...)
	record.modelUsage = step.Metadata.ModelUsage
	record.toolStep = isToolStep(step)
	spanID := record.spanID
	if spanID == "" {
		spanID = step.Metadata.ToolCall.ID
		if spanID == "" {
			spanID = fmt.Sprintf("antigravity-step-%d", index)
		}
		record.spanID = spanID
	}
	a.Mu.Unlock()

	if step.Type == contracts.AntigravityStepTypePlannerResponse {
		a.applyPlannerResponse(state, index, step)
	}
	if step.Type == contracts.AntigravityStepTypeInvokeSubagent {
		a.linkSubagentResults(state, index, step)
	}
	if step.Type == contracts.AntigravityStepTypeErrorMessage && !isOpenStepStatus(step.Status) {
		a.Mu.Lock()
		state.heldErrorStep = append(json.RawMessage(nil), raw...)
		state.retryPending = false
		a.Mu.Unlock()
	}
	if !a.acceptsCascadeUpdate(state) {
		return nil
	}
	if step.Status == contracts.AntigravityStepStatusWaiting {
		a.registerInteraction(state, index, step)
	} else if previousStatus == contracts.AntigravityStepStatusWaiting {
		a.withdrawInteraction(state, index)
	}
	if isOpenStepStatus(step.Status) {
		if isToolStep(step) {
			state.sink.OpenSpan(spanID, "")
			if err := a.persistLiveStep(state, index, step, raw, spanID); err != nil {
				return err
			}
			if output := step.RunCommand.CombinedOutput.Full; output != "" {
				state.sink.ReportProgress(agent.OutputTailProgress(spanID, output, false))
			}
		}
		return nil
	}
	return a.persistStep(state, index, step, raw)
}

func (a *Agent) persistLiveStep(state *cascadeState, index int, step antigravityStep, raw json.RawMessage, spanID string) error {
	if a.IsDiscardingOutput() {
		return nil
	}
	a.Mu.Lock()
	record := state.steps[index]
	if record == nil || record.liveOutputAccepted {
		a.Mu.Unlock()
		return nil
	}
	record.liveOutputAccepted = true
	a.Mu.Unlock()
	cleaned, err := scrubStep(raw)
	if err != nil {
		a.Mu.Lock()
		record.liveOutputAccepted = false
		a.Mu.Unlock()
		slog.Error("antigravity scrub live step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
		return err
	}
	err = state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: cleaned, AgentSessionID: state.id, IdempotencyKey: fmt.Sprintf("antigravity:%s:%d:live", state.id, index),
	}, agent.SpanInfo{SpanID: spanID, SpanType: contracts.AntigravityFrameStep})
	if !agent.OutputRetained(err) {
		a.Mu.Lock()
		record.liveOutputAccepted = false
		a.Mu.Unlock()
		slog.Error("antigravity persist live step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
		return err
	}
	return nil
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

func (a *Agent) applyPlannerResponse(state *cascadeState, index int, step antigravityStep) {
	text := step.PlannerResponse.ModifiedResponse
	if text == "" {
		text = step.PlannerResponse.Response
	}
	scope := fmt.Sprintf("antigravity:%s:%d", state.id, index)
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return
	}
	record := state.steps[index]
	previous := record.text
	record.text = text
	retryPending := state.retryPending
	state.retryPending = false
	state.heldErrorStep = nil
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

func (a *Agent) persistStep(state *cascadeState, index int, step antigravityStep, raw json.RawMessage) error {
	if a.IsDiscardingOutput() {
		return nil
	}
	a.Mu.Lock()
	record := state.steps[index]
	if record == nil {
		record = &stepState{status: step.Status, toolStep: isToolStep(step)}
		state.steps[index] = record
	}
	if record.consumed {
		a.Mu.Unlock()
		return nil
	}
	interrupted := state.interrupted
	spanID := ""
	if record != nil && isToolStep(step) {
		spanID = record.spanID
	}
	a.Mu.Unlock()

	switch step.Type {
	case contracts.AntigravityStepTypeUserInput:
		if state.parent == nil || slices.Contains(step.UserInput.Tags, antigravityQueueInputTag) {
			a.consumeStep(state, index)
			return nil
		}
	case contracts.AntigravityStepTypeSystemMessage:
		message := step.SystemMessage.AgentMessage
		if message == nil || message.HideFromUser || message.Content == "" {
			a.consumeStep(state, index)
			return nil
		}
	case contracts.AntigravityStepTypeErrorMessage:
		a.consumeStep(state, index)
		return nil
	}
	cleaned, err := scrubStep(raw)
	if err != nil {
		slog.Error("antigravity scrub step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
		return err
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
			return splitErr
		}
		thinking := content
		thinking.Original = thinkingRaw
		thinking.IdempotencyKey += ":thinking"
		contents = []agent.MessageContent{thinking, content}
	}
	messages := make([]agent.TranscriptMessage, 0, len(contents))
	for _, item := range contents {
		messages = append(messages, agent.TranscriptMessage{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: item, Span: span})
	}
	if err := agent.PersistTranscriptBatch(state.sink, messages); !agent.OutputRetained(err) {
		slog.Error("antigravity persist step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
		return err
	}
	a.consumeStep(state, index)
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
	return nil
}

func (a *Agent) consumeStep(state *cascadeState, index int) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if record := state.steps[index]; record != nil {
		record.consumed = true
		record.raw = nil
		record.text = ""
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

func (a *Agent) persistUnknownStep(state *cascadeState, index int, raw json.RawMessage) error {
	if a.IsDiscardingOutput() {
		return nil
	}
	a.Mu.Lock()
	record := state.steps[index]
	if record != nil && record.consumed {
		a.Mu.Unlock()
		return nil
	}
	if record == nil {
		record = &stepState{}
		state.steps[index] = record
	}
	record.raw = append(json.RawMessage(nil), raw...)
	a.Mu.Unlock()
	if err := state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: append(json.RawMessage(nil), raw...), AgentSessionID: state.id,
		IdempotencyKey: fmt.Sprintf("antigravity:%s:%d", state.id, index),
	}, agent.SpanInfo{}); !agent.OutputRetained(err) {
		slog.Error("antigravity persist unknown step", "agent_id", a.AgentID(), "error", err)
		return err
	}
	a.consumeStep(state, index)
	return nil
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
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return
	}
	if state.retryPending {
		a.Mu.Unlock()
		return
	}
	state.retryPending = true
	state.heldErrorStep = nil
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
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return
	}
	state.contextWindow = usage.MaxContextTokens
	state.contextTokens = usage.EstimatedTokensUsed
	a.Mu.Unlock()
	values := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{})
	values[contracts.ContextUsageFieldContextWindow] = usage.MaxContextTokens
	values[contracts.ContextUsageFieldContextTokens] = usage.EstimatedTokensUsed
	state.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: values})
}

func (a *Agent) endTurn(state *cascadeState, completion agent.MessageCompletion) (err error) {
	a.Mu.Lock()
	if !a.cascadeInstalledLocked(state) || state.turnPhase != cascadeTurnActive {
		a.Mu.Unlock()
		return nil
	}
	state.turnPhase = cascadeTurnFinalizing
	if state.interrupted {
		completion = agent.MessageCompletionInterrupted
	} else if len(state.heldErrorStep) > 0 {
		completion = agent.MessageCompletionError
	}
	heldErrorStep := append(json.RawMessage(nil), state.heldErrorStep...)
	a.Mu.Unlock()
	defer func() {
		if err != nil {
			a.Mu.Lock()
			if a.cascadeInstalledLocked(state) {
				state.turnPhase = cascadeTurnActive
			}
			a.Mu.Unlock()
		}
	}()

	if !a.IsDiscardingOutput() {
		if err := a.flushOpenSteps(state, completion); err != nil {
			return err
		}
	}
	if !a.cascadeInstalled(state) {
		return nil
	}
	if !a.IsDiscardingOutput() && state.parent == nil && completion == agent.MessageCompletionComplete {
		a.maybePublishPlan(state)
	}
	if !a.cascadeInstalled(state) {
		return nil
	}
	if !a.IsDiscardingOutput() && len(heldErrorStep) > 0 {
		raw, scrubErr := scrubStep(heldErrorStep)
		if scrubErr != nil {
			return scrubErr
		}
		if _, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, Completion: agent.MessageCompletionError, AgentSessionID: state.id}); !agent.OutputRetained(err) {
			slog.Error("antigravity persist agent error", "agent_id", a.AgentID(), "error", err)
			return err
		}
	}
	if !a.cascadeInstalled(state) {
		return nil
	}
	if state.parent != nil {
		status := bgtask.StatusSucceeded
		switch completion {
		case agent.MessageCompletionInterrupted:
			status = bgtask.StatusStopped
		case agent.MessageCompletionError:
			status = bgtask.StatusFailed
		case agent.MessageCompletionComplete, agent.MessageCompletionFinished:
		}
		a.closeChildRow(state, status)
	} else if !a.IsDiscardingOutput() {
		frame := a.turnEndFrame(state, completion)
		if err := state.sink.PersistTurnEnd(frame, agent.SpanInfo{}); !agent.OutputRetained(err) {
			slog.Error("antigravity persist turn end", "agent_id", a.AgentID(), "error", err)
			return err
		}
	}
	a.Mu.Lock()
	if !a.cascadeInstalledLocked(state) {
		a.Mu.Unlock()
		return nil
	}
	backgroundOpen := len(state.background) > 0
	a.Mu.Unlock()
	if !backgroundOpen {
		state.sink.ResetSpans()
	}
	if !a.cascadeInstalled(state) {
		return nil
	}
	state.sink.ReportProgress(agent.ResetProgress())
	a.Mu.Lock()
	if !a.cascadeInstalledLocked(state) {
		a.Mu.Unlock()
		return nil
	}
	state.turnPhase = cascadeTurnIdle
	state.sendInFlight = false
	state.heldErrorStep = nil
	state.retryPending = false
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	if state.parent != nil {
		providerkit.PublishSteerableTurnActiveTo(state.sink, false, seq)
	} else {
		providerkit.PublishTurnStateTo(a.sink, agent.TurnState{}, seq)
		if a.cascadeCurrent(state) && !a.IsDiscardingOutput() {
			a.quotaWork.Run("quota", a.refreshRateLimits)
		}
	}
	return nil
}

func (a *Agent) flushOpenSteps(state *cascadeState, completion agent.MessageCompletion) error {
	a.Mu.Lock()
	type pending struct {
		index int
		raw   json.RawMessage
	}
	var pendingSteps []pending
	for index := range state.turnSteps {
		record := state.steps[index]
		if record == nil || record.consumed || len(record.raw) == 0 {
			continue
		}
		if _, background := state.background[index]; background {
			continue
		}
		pendingSteps = append(pendingSteps, pending{index: index, raw: append(json.RawMessage(nil), record.raw...)})
	}
	a.Mu.Unlock()
	for _, item := range pendingSteps {
		step, err := decodeStep(item.raw)
		if err != nil {
			if err := a.persistUnknownStep(state, item.index, item.raw); err != nil {
				return err
			}
			continue
		}
		if completion != agent.MessageCompletionComplete && isOpenStepStatus(step.Status) {
			step.Status = contracts.AntigravityStepStatusCanceled
		}
		if err := a.persistStep(state, item.index, step, item.raw); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) turnEndFrame(state *cascadeState, completion agent.MessageCompletion) agent.MessageContent {
	a.Mu.Lock()
	uses := 0
	usage := map[string]int64{}
	for index := range state.turnSteps {
		record := state.steps[index]
		if record == nil {
			continue
		}
		if record.toolStep && record.consumed {
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
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return
	}
	generationCtx := a.streamCtx
	state.closed = true
	state.connectErr = streamErr
	state.sendInFlight = false
	rootBeforeStart := state.parent == nil && !state.seenUpdate
	active := state.turnPhase != cascadeTurnIdle
	hasOpenState := state.rowOpen || len(state.controls) > 0 || len(state.background) > 0
	seq := a.NextTurnSeq()
	state.connectedOnce.Do(func() { close(state.connected) })
	a.Mu.Unlock()
	if rootBeforeStart {
		return
	}
	if state.parent != nil {
		if err := a.rpc.cancel(generationCtx, state.id); err != nil {
			slog.Warn("antigravity cancel a failed child invocation", "agent_id", a.AgentID(), "cascade_id", state.id, "error", err)
		}
		a.Mu.Lock()
		current := a.cascadeCurrentLocked(state) && state.closed
		a.Mu.Unlock()
		if !current {
			return
		}
	}
	text := fmt.Sprintf("Antigravity CLI %s lost the language-server stream: %s", a.version, streamErrorText(streamErr))
	if state.parent == nil {
		if !a.IsDiscardingOutput() {
			state.sink.PersistLeapMuxNotification(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldText: text})
		}
		if !a.cascadeCurrent(state) {
			return
		}
		if err := a.endTurn(state, agent.MessageCompletionError); err != nil {
			slog.Error("antigravity finish failed stream", "agent_id", a.AgentID(), "error", err)
		}
		go a.stop(state)
		return
	}
	if active || hasOpenState {
		if !a.IsDiscardingOutput() {
			state.sink.PersistLeapMuxNotification(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldText: text})
		}
		if !a.cascadeCurrent(state) {
			return
		}
		if err := a.endTurn(state, agent.MessageCompletionError); err != nil {
			slog.Error("antigravity finish failed child stream", "agent_id", a.AgentID(), "error", err)
		}
		a.retireAllInteractions(state)
		a.closeChildRow(state, bgtask.StatusFailed)
		a.closeBackgroundTasks(state, bgtask.StatusEndedWithUnknownOutcome)
		providerkit.PublishSteerableTurnActiveTo(state.sink, false, seq)
	}
}

func (a *Agent) closeChildRow(state *cascadeState, status bgtask.Status) {
	queue := a.sink.OutputQueue()
	a.Mu.Lock()
	if !state.rowOpen {
		a.Mu.Unlock()
		return
	}
	queue.Append(backgroundTaskCloseWrite(a.sink, state.id, status))
	state.rowOpen = false
	a.Mu.Unlock()
	a.flushBackgroundTaskWrites(queue)
}
