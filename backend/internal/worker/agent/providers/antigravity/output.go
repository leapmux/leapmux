package antigravity

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func stepMessageKey(cascadeID string, index int) string {
	return fmt.Sprintf("antigravity:%s:%d", cascadeID, index)
}

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
	record.executionID = step.Metadata.ExecutionID
	record.plannerResponse = step.Type == contracts.AntigravityStepTypePlannerResponse
	replyText, _ := step.PlannerResponse.visibleContent()
	record.replyText = record.plannerResponse && replyText != ""
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
			if err := a.persistLiveStep(state, index, raw, spanID); err != nil {
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

func (a *Agent) persistLiveStep(state *cascadeState, index int, raw json.RawMessage, spanID string) error {
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
	cleaned, err := scrubStep(raw, "")
	if err != nil {
		a.Mu.Lock()
		record.liveOutputAccepted = false
		a.Mu.Unlock()
		slog.Error("antigravity scrub live step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", index, "error", err)
		return err
	}
	err = providerkit.OpenToolSpan(state.sink, agent.MessageContent{
		Original: cleaned, AgentSessionID: state.id, IdempotencyKey: stepMessageKey(state.id, index) + ":live",
	}, spanID, contracts.AntigravityFrameStep, false)
	if !agent.OutputRetained(err) {
		state.sink.CloseSpan(spanID)
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

func (response antigravityPlannerResponse) visibleContent() (text, thinking string) {
	text = response.ModifiedResponse
	if blankPlannerText(text) {
		text = response.Response
		if blankPlannerText(text) {
			text = ""
		}
	}
	thinking = response.Thinking
	if blankPlannerText(thinking) {
		thinking = ""
	}
	return text, thinking
}

// Treat Unicode whitespace and the byte order mark as blank in both renderers.
func blankPlannerText(text string) bool {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }) == ""
}

func (a *Agent) applyPlannerResponse(state *cascadeState, index int, step antigravityStep) {
	text, thinking := step.PlannerResponse.visibleContent()
	recovered := text != "" || thinking != "" ||
		step.Status == contracts.AntigravityStepStatusDone && len(step.PlannerResponse.ToolCalls) > 0
	scope := fmt.Sprintf("antigravity:%s:%d", state.id, index)
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return
	}
	record := state.steps[index]
	previous := record.text
	record.text = text
	resolveRetry := recovered && state.retryPending
	if recovered {
		state.retryPending = false
		state.heldErrorStep = nil
	}
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
	if resolveRetry {
		persistCascadeNotification(state, map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved})
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
	manualCheckpoint := step.Type == contracts.AntigravityStepTypeCheckpoint && step.Metadata.ExecutionID != "" &&
		state.manualCompactionExecutionID != nil && *state.manualCompactionExecutionID == step.Metadata.ExecutionID
	spanID := ""
	if record != nil && isToolStep(step) {
		spanID = record.spanID
	}
	a.Mu.Unlock()

	splitPlannerResponse := false
	compactionTrigger := ""
	switch step.Type {
	case contracts.AntigravityStepTypePlannerResponse:
		text, thinking := step.PlannerResponse.visibleContent()
		if text == "" && thinking == "" {
			a.finalizeStep(state, index, step, spanID)
			return nil
		}
		splitPlannerResponse = text != "" && thinking != ""
	case contracts.AntigravityStepTypeUserInput:
		if state.parent == nil && step.Status == contracts.AntigravityStepStatusDone {
			for _, item := range step.UserInput.Items {
				info := item.Item.SlashCommand.Info
				if info.Name == antigravityCompactCommand && info.Type == antigravitySystemSlashCommand {
					a.beginManualCompaction(state, step.Metadata.ExecutionID)
					break
				}
			}
		}
		if err := a.bindInputEcho(state, index, step); err != nil {
			return err
		}
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
	case contracts.AntigravityStepTypeCheckpoint:
		if step.Status != contracts.AntigravityStepStatusDone {
			a.consumeStep(state, index)
			if manualCheckpoint {
				outcome := providerkit.CompactionEndFailed
				if step.Status == contracts.AntigravityStepStatusCanceled {
					outcome = providerkit.CompactionEndAborted
				}
				a.endManualCompaction(state, outcome)
			}
			return nil
		}
		if manualCheckpoint {
			compactionTrigger = "manual"
		}
	}
	cleaned, err := scrubStep(raw, compactionTrigger)
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
		IdempotencyKey: stepMessageKey(state.id, index),
		NativeTurnID:   step.Metadata.ExecutionID,
	}
	contents := []agent.MessageContent{content}
	if splitPlannerResponse {
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
	a.finalizeStep(state, index, step, spanID)
	if manualCheckpoint {
		a.endManualCompaction(state, providerkit.CompactionEndCompleted)
	}
	return nil
}

func (a *Agent) beginManualCompaction(state *cascadeState, executionID string) {
	publication := state.sink.CaptureMessage(agent.MessageContent{AgentSessionID: state.id}, agent.SpanInfo{}).Publication
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed || state.manualCompactionExecutionID != nil {
		a.Mu.Unlock()
		return
	}
	state.manualCompactionExecutionID = &executionID
	a.Mu.Unlock()
	if publication != nil {
		publication.Owner().PublishSessionInfo(map[string]any{contracts.SessionInfoKeyCompaction: map[string]any{contracts.CompactionFieldTrigger: "manual"}})
	}
}

func (a *Agent) endManualCompaction(state *cascadeState, outcome providerkit.CompactionEndOutcome) {
	a.Mu.Lock()
	wasCompacting := state.manualCompactionExecutionID != nil
	state.manualCompactionExecutionID = nil
	a.Mu.Unlock()
	if wasCompacting {
		if outcome != providerkit.CompactionEndCompleted && !a.IsDiscardingOutput() {
			persistCascadeNotification(state, map[string]string{
				contracts.NotificationFieldType:    contracts.NotificationTypeCompactionEnded,
				contracts.NotificationFieldOutcome: string(outcome),
			})
		}
		if publication := state.sink.CaptureMessage(agent.MessageContent{AgentSessionID: state.id}, agent.SpanInfo{}).Publication; publication != nil {
			publication.Owner().PublishSessionInfo(map[string]any{contracts.SessionInfoKeyCompaction: nil})
		}
	}
}

func persistCascadeNotification(state *cascadeState, fields map[string]string) {
	raw, _ := json.Marshal(fields)
	if _, err := state.sink.PersistWorkerNotification(agent.MessageContent{Original: raw, AgentSessionID: state.id}); !agent.OutputRetained(err) {
		slog.Warn("Antigravity could not retain a cascade notification", "cascade_id", state.id, "error", err)
	}
}

func (a *Agent) finalizeStep(state *cascadeState, index int, step antigravityStep, spanID string) {
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
	for _, background := range a.backgroundForStep(state, index) {
		a.finishBackgroundStep(state, background, step)
	}
	if state.parent == nil && step.Status == contracts.AntigravityStepStatusDone {
		a.recordPlanStep(state, step)
	}
}

// consumeStep records that the transcript holds the step in its final form.
// The provider reads no later frame of a consumed step (applyUpdate), so no
// later frame can withdraw the step's control either. consumeStep withdraws
// it, so no request stays visible, or appears later, for a step that the
// provider no longer follows.
func (a *Agent) consumeStep(state *cascadeState, index int) {
	a.Mu.Lock()
	if record := state.steps[index]; record != nil {
		record.consumed = true
		record.raw = nil
		record.text = ""
	}
	control := state.controls[index]
	delete(state.controls, index)
	a.Mu.Unlock()
	a.retireControl(state, control)
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
	record.status = ""
	record.raw = append(json.RawMessage(nil), raw...)
	a.Mu.Unlock()
	payload := append(json.RawMessage(nil), raw...)
	if cleaned, err := scrubStep(raw, ""); err == nil {
		payload = cleaned
	}
	if err := state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: payload, AgentSessionID: state.id,
		IdempotencyKey: stepMessageKey(state.id, index),
	}, agent.SpanInfo{}); !agent.OutputRetained(err) {
		slog.Error("antigravity persist unknown step", "agent_id", a.AgentID(), "error", err)
		return err
	}
	a.consumeStep(state, index)
	return nil
}

func scrubStep(raw json.RawMessage, compactionTrigger string) (json.RawMessage, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	scrubOpaqueFields(value)
	if compactionTrigger != "" {
		value[contracts.CompactionFieldTrigger] = compactionTrigger
	}
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

func (a *Agent) endTurn(state *cascadeState, completion agent.MessageCompletion) error {
	return a.endTurnWithTeardown(state, completion, false)
}

func (a *Agent) endTurnWithTeardown(state *cascadeState, completion agent.MessageCompletion, teardown bool) (err error) {
	// Only the native idle frame ends a turn with a complete observation.
	nativeIdle := !teardown && completion == agent.MessageCompletionComplete
	a.Mu.Lock()
	if !a.cascadeInstalledLocked(state) || state.turnPhase != cascadeTurnActive {
		a.Mu.Unlock()
		return nil
	}
	state.turnPhase = cascadeTurnFinalizing
	interrupted := state.interrupted
	if interrupted {
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

	includeToolSteps := interrupted || teardown
	if !a.IsDiscardingOutput() {
		if err := a.flushOpenSteps(state, completion, includeToolSteps); err != nil {
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
		raw, scrubErr := scrubStep(heldErrorStep, "")
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
	compactionOutcome := providerkit.CompactionEndCompleted
	switch completion {
	case agent.MessageCompletionInterrupted:
		compactionOutcome = providerkit.CompactionEndAborted
	case agent.MessageCompletionError:
		compactionOutcome = providerkit.CompactionEndFailed
	case agent.MessageCompletionComplete, agent.MessageCompletionFinished:
	}
	a.endManualCompaction(state, compactionOutcome)
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
	a.finishInputReplies(state, nativeIdle, completion)
	a.Mu.Lock()
	if !a.cascadeInstalledLocked(state) {
		a.Mu.Unlock()
		return nil
	}
	backgroundOpen := len(state.background) > 0
	a.Mu.Unlock()
	if includeToolSteps {
		if !backgroundOpen || teardown {
			providerkit.ResetSpansAndProgress(state.sink)
		} else {
			state.sink.ReportProgress(agent.ResetModelProgress())
		}
	} else {
		state.sink.ReportProgress(agent.ResetModelProgress())
	}
	a.Mu.Lock()
	if !a.cascadeInstalledLocked(state) {
		a.Mu.Unlock()
		return nil
	}
	state.turnPhase = cascadeTurnIdle
	state.sendInFlight = false
	state.interrupted = false
	state.heldErrorStep = nil
	state.retryPending = false
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	if state.parent != nil {
		providerkit.PublishSteerableTurnActiveTo(state.sink, false, seq)
	} else {
		providerkit.PublishTurnStateTo(a.sink, agent.TurnState{}, seq)
		if a.cascadeCurrent(state) && !a.IsDiscardingOutput() {
			a.accountRefreshWork.Run("catalog", a.refreshAccountCatalog)
			a.accountRefreshWork.Run("quota", a.refreshAccountQuota)
		}
	}
	return nil
}

func (a *Agent) flushOpenSteps(state *cascadeState, completion agent.MessageCompletion, includeToolSteps bool) error {
	a.Mu.Lock()
	type pending struct {
		index int
		raw   json.RawMessage
	}
	var pendingSteps []pending
	collect := func(index int, record *stepState) {
		if record == nil || record.consumed || len(record.raw) == 0 {
			return
		}
		if !includeToolSteps && record.toolStep && record.status != contracts.AntigravityStepStatusWaiting && state.controls[index] == nil {
			return
		}
		if hasBackgroundForStepLocked(state, index) {
			return
		}
		pendingSteps = append(pendingSteps, pending{index: index, raw: append(json.RawMessage(nil), record.raw...)})
	}
	if includeToolSteps {
		for index, record := range state.steps {
			collect(index, record)
		}
	} else {
		for index := range state.turnSteps {
			collect(index, state.steps[index])
		}
	}
	a.Mu.Unlock()
	slices.SortFunc(pendingSteps, func(left, right pending) int {
		return cmp.Compare(left.index, right.index)
	})
	for _, item := range pendingSteps {
		step, err := decodeStep(item.raw)
		if err != nil {
			slog.Warn("antigravity decode open step failed; persisting raw step", "agent_id", a.AgentID(), "cascade_id", state.id, "step_index", item.index, "error", err)
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
			usage[key] += value
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
	if state.parent == nil && !rootBeforeStart && a.fatalStreamErr == nil {
		a.fatalStreamErr = streamErr
	}
	active := state.turnPhase != cascadeTurnIdle
	hasOpenState := state.rowOpen || len(state.inputReplies) > 0 || len(state.controls) > 0 || len(state.background) > 0 || hasOpenToolStepsLocked(state)
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
		a.retireAllInteractions(state)
		a.closeBackgroundTasks(state, bgtask.StatusFailed)
		if err := a.finishCascadeOutput(state, agent.MessageCompletionError); err != nil {
			slog.Error("antigravity finish failed stream", "agent_id", a.AgentID(), "error", err)
		}
		go a.stop(state, false)
		return
	}
	if active || hasOpenState {
		if !a.IsDiscardingOutput() {
			state.sink.PersistLeapMuxNotification(map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeAgentError, contracts.NotificationFieldText: text})
		}
		if !a.cascadeCurrent(state) {
			return
		}
		a.retireAllInteractions(state)
		a.closeBackgroundTasks(state, bgtask.StatusEndedWithUnknownOutcome)
		if err := a.finishCascadeOutput(state, agent.MessageCompletionError); err != nil {
			slog.Error("antigravity finish failed child stream", "agent_id", a.AgentID(), "error", err)
		}
		a.finishInputReplies(state, false, agent.MessageCompletionError)
		a.closeChildRow(state, bgtask.StatusFailed)
		providerkit.PublishSteerableTurnActiveTo(state.sink, false, seq)
	}
}

func hasOpenToolStepsLocked(state *cascadeState) bool {
	for _, record := range state.steps {
		if record != nil && !record.consumed && record.toolStep && len(record.raw) > 0 {
			return true
		}
	}
	return false
}

func (a *Agent) closeIdleChildOnUpdate(state *cascadeState) error {
	a.Mu.Lock()
	if !a.cascadeInstalledLocked(state) || state.parent == nil || (!state.rowOpen && len(state.inputReplies) == 0) || state.turnPhase != cascadeTurnIdle {
		a.Mu.Unlock()
		return nil
	}
	status := bgtask.StatusSucceeded
	completion := agent.MessageCompletionComplete
	if state.interrupted {
		status = bgtask.StatusStopped
		completion = agent.MessageCompletionInterrupted
	} else if len(state.heldErrorStep) > 0 {
		status = bgtask.StatusFailed
		completion = agent.MessageCompletionError
	}
	heldErrorStep := append(json.RawMessage(nil), state.heldErrorStep...)
	a.Mu.Unlock()
	if !a.IsDiscardingOutput() && len(heldErrorStep) > 0 {
		raw, err := scrubStep(heldErrorStep, "")
		if err != nil {
			return err
		}
		if _, err := state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, Completion: agent.MessageCompletionError, AgentSessionID: state.id}); !agent.OutputRetained(err) {
			slog.Error("antigravity persist agent error", "agent_id", a.AgentID(), "error", err)
			return err
		}
	}
	if !a.cascadeInstalled(state) {
		return nil
	}
	a.closeChildRow(state, status)
	a.finishInputReplies(state, true, completion)
	a.Mu.Lock()
	if a.cascadeInstalledLocked(state) {
		state.heldErrorStep = nil
		state.retryPending = false
		state.interrupted = false
	}
	a.Mu.Unlock()
	return nil
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
