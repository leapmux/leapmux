package letta

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Letta Code's conversation output.
//
// Every stream_delta carries a `message_type` and a runtime scope. The worker
// persists native payloads and gives each native tool call its own row. The
// browser plugin reads the same `message_type`. The worker also assembles text
// and thinking rows.

// lettaAttachmentLabel identifies the provider in an attachment refusal.
const lettaAttachmentLabel = "Letta Code"

// lettaFinishedTurnLimit limits the recent native ends that suppress delayed repeats.
const lettaFinishedTurnLimit = 16

// isLettaRawInterrupt reports whether a raw frame is LeapMux's own interrupt
// marker for Letta.
func isLettaRawInterrupt(content []byte) bool {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(content, &head); err != nil {
		return false
	}
	return head.Kind == "abort_message"
}

// lettaDelta is the payload of one stream_delta. `OTID` is the id that a
// `user_message` repeats from its sender: Letta Code takes it from the
// `client_message_id` of the message (see protocol.go).
type lettaDelta struct {
	ID               string                `json:"id"`
	RunID            string                `json:"run_id"`
	Status           string                `json:"status"`
	MessageType      string                `json:"message_type"`
	Content          json.RawMessage       `json:"content"`
	Reasoning        string                `json:"reasoning"`
	ToolCallID       string                `json:"tool_call_id"`
	ToolCalls        []lettaNativeToolCall `json:"tool_calls"`
	ToolCall         *lettaNativeToolCall  `json:"tool_call"`
	ToolName         string                `json:"tool_name"`
	ToolInput        json.RawMessage       `json:"tool_input"`
	ToolReturn       json.RawMessage       `json:"tool_return"`
	ToolReturns      json.RawMessage       `json:"tool_returns"`
	OTID             string                `json:"otid"`
	PromptTokens     *int64                `json:"prompt_tokens"`
	CompletionTokens *int64                `json:"completion_tokens"`
	TotalTokens      *int64                `json:"total_tokens"`
	ContextTokens    json.RawMessage       `json:"context_tokens"`
	EventType        string                `json:"event_type"`
	EventData        json.RawMessage       `json:"event_data"`
	IsFinal          *bool                 `json:"is_terminal"`
	Message          string                `json:"message"`
	Level            string                `json:"level"`
}

type lettaNativeToolCall struct {
	CallID    string          `json:"tool_call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// onStreamDelta dispatches one stream_delta payload.
func (a *Agent) onStreamDelta(payload []byte, subagentID string, scope *runtimeScope) {
	kind := providerkit.StringField(payload, contracts.LettaDeltaFieldMessageType)
	switch kind {
	case lettaDeltaApprovalRequestMessage, lettaDeltaApprovalClassificationEnd, lettaDeltaErrorMessage:
		return
	}
	target, ok := a.toolOutputTarget(subagentID, kind)
	if !ok {
		slog.Debug("letta ignored a delta without an active target", "agent_id", a.AgentID(), "subagent_id", subagentID)
		return
	}
	var delta lettaDelta
	if err := json.Unmarshal(payload, &delta); err != nil {
		slog.Debug("letta: bad stream delta", "agent_id", a.AgentID(), "error", err)
		a.persistUnknownDelta(target, payload, scope)
		return
	}

	switch delta.MessageType {
	case contracts.LettaDeltaKindAssistantMessage:
		a.onAssistantDelta(&delta, subagentID)
	case contracts.LettaDeltaKindReasoningMessage:
		a.onReasoningDelta(&delta, subagentID)
	case contracts.LettaDeltaKindToolReturnMessage:
		if subagentID == "" && !isLettaToolProgress(&delta) {
			a.rememberChildTaskReceipt(&delta)
		}
		a.onToolReturn(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindClientToolStart:
		a.onToolStart(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindToolCallMessage:
		a.onNativeToolCalls(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindClientToolEnd:
		a.onClientToolEnd(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindUserMessage:
		// Letta Code echoes a message that it queued when the message starts, and it
		// gives the echo the id of that message. LeapMux stored each message that it
		// sent: the reader's text as a user row, and the answer to a question as a
		// control response. An echo that states a LeapMux id therefore draws no second
		// row. It is also no notification of a child, because the message is the
		// reader's own text. An answer that another sender wrote states another id and
		// stays a row.
		if isLeapMuxClientMessageID(delta.OTID) {
			return
		}
		text := deltaText(delta.Content)
		if subagentID == "" {
			a.observeChildTaskNotifications(text)
		}
		if subagentID == "" || text != a.children[subagentID].prompt {
			a.persistRowTo(target, payload, agent.SpanInfo{})
		}
	case contracts.LettaDeltaKindRetry:
		a.handleRetry(payload, target, subagentID)
	case contracts.LettaDeltaKindEventMessage:
		switch delta.EventType {
		case contracts.LettaEventTypeRetry:
			a.handleRetry(payload, target, subagentID)
		case contracts.LettaEventTypeCompaction:
			a.startCompaction(target, subagentID, strings.TrimSpace(providerkit.StringField(delta.EventData, contracts.LettaDeltaFieldTrigger)))
		default:
			a.persistUnknownDelta(target, payload, scope)
		}
	case contracts.LettaDeltaKindSummaryMessage:
		a.flushGenerationFor(subagentID, agent.MessageCompletionComplete)
		a.endCompaction(target, subagentID)
		a.persistNoticeTo(target, payload)
	case contracts.LettaDeltaKindLoopError:
		a.handleLoopError(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindUsageStatistics:
		publishLettaUsage(target, &delta)
	case contracts.LettaDeltaKindStatus:
		if lettaWarningLevel(delta.Level) && delta.Message != "Interrupted" && strings.TrimSpace(delta.Message) != "" {
			a.persistNoticeTo(target, payload)
		}
	case contracts.LettaDeltaKindStopReason:
		// Protocol state. `turn_finished` already states the stop reason as a
		// result divider.
	default:
		a.persistUnknownDelta(target, payload, scope)
	}
}

func (a *Agent) ownsRuntime(scope *runtimeScope) bool {
	if scope == nil || scope.AgentID == "" || scope.ConversationID == "" {
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return scope.AgentID == a.agentID && scope.ConversationID == a.conversationID
}

func (a *Agent) persistUnknownDelta(target agent.ProviderServices, payload []byte, scope *runtimeScope) {
	if !a.ownsRuntime(scope) {
		slog.Debug("letta ignored an unknown delta outside the current runtime", "agent_id", a.AgentID())
		return
	}
	a.persistRowTo(target, payload, agent.SpanInfo{})
}

func (a *Agent) persistUnknownFrame(payload []byte) {
	var frame struct {
		Runtime *runtimeScope `json:"runtime"`
	}
	if json.Unmarshal(payload, &frame) != nil || !a.ownsRuntime(frame.Runtime) {
		slog.Debug("letta ignored an unknown frame outside the current runtime", "agent_id", a.AgentID())
		return
	}
	a.persistRowTo(a.sink, payload, agent.SpanInfo{})
}

func lettaWarningLevel(level string) bool {
	return level == "warning" || level == "error"
}

func (a *Agent) startCompaction(target agent.ProviderServices, id, trigger string) {
	if a.compactionLive[id] {
		return
	}
	a.flushGenerationFor(id, agent.MessageCompletionComplete)
	if a.compactionLive == nil {
		a.compactionLive = make(map[string]bool)
	}
	a.compactionLive[id] = true
	providerkit.PublishCompaction(target, trigger)
}

func (a *Agent) endCompaction(target agent.ProviderServices, id string) {
	delete(a.compactionLive, id)
	providerkit.EndCompaction(target)
}

func (a *Agent) finishOpenCompaction(target agent.ProviderServices, id string, completion agent.MessageCompletion) {
	if !a.compactionLive[id] {
		return
	}
	a.endCompaction(target, id)
	outcome := providerkit.CompactionEndFailed
	if completion == agent.MessageCompletionInterrupted {
		outcome = providerkit.CompactionEndAborted
	}
	providerkit.PersistCompactionEnded(target, outcome, "")
}

func (a *Agent) retryForTarget(id string) *providerkit.RetryResolution {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.retries == nil {
		a.retries = make(map[string]*providerkit.RetryResolution)
	}
	if a.retries[id] == nil {
		a.retries[id] = &providerkit.RetryResolution{}
	}
	return a.retries[id]
}

func (a *Agent) handleRetry(payload []byte, target agent.ProviderServices, id string) {
	kept := a.heldErrors[id][:0]
	for _, held := range a.heldErrors[id] {
		if held.final {
			kept = append(kept, held)
		}
	}
	if len(kept) == 0 {
		delete(a.heldErrors, id)
	} else {
		a.heldErrors[id] = kept
	}
	scope := lettaGenerationScope(id)
	providerkit.DiscardGenerationScopes(&a.generation, target, []string{scope, scope + ":reasoning"})
	if _, err := target.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload}); err != nil {
		slog.Error("letta could not persist a retry", "agent_id", a.AgentID(), "error", err)
	}
	a.retryForTarget(id).NoteRetry()
}

type lettaHeldError struct {
	payload []byte
	final   bool
}

// handleLoopError retains root errors until the native turn end supplies its reason.
// A child supplies no equivalent error-bearing divider, so its final error persists at once.
func (a *Agent) handleLoopError(payload []byte, delta *lettaDelta, target agent.ProviderServices, id string) {
	final := delta.IsFinal != nil && *delta.IsFinal
	if a.heldErrors == nil {
		a.heldErrors = make(map[string][]lettaHeldError)
	}
	if final && id != "" {
		a.flushGenerationFor(id, agent.MessageCompletionError)
		a.flushHeldErrors(target, id, delta.Message)
		a.persistNoticeTo(target, payload)
		return
	}
	kept := a.heldErrors[id][:0]
	for _, held := range a.heldErrors[id] {
		if delta.Message != "" && providerkit.StringField(held.payload, contracts.LettaDeltaFieldMessage) == delta.Message {
			continue
		}
		kept = append(kept, held)
	}
	a.heldErrors[id] = append(kept, lettaHeldError{payload: bytes.Clone(payload), final: final})
}

func (a *Agent) flushHeldErrors(target agent.ProviderServices, id, reason string) {
	held := a.heldErrors[id]
	delete(a.heldErrors, id)
	for _, entry := range held {
		if reason != "" && providerkit.StringField(entry.payload, contracts.LettaDeltaFieldMessage) == reason {
			continue
		}
		a.persistNoticeTo(target, entry.payload)
	}
}

func (a *Agent) persistNoticeTo(target agent.ProviderServices, payload []byte) {
	if _, err := target.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload}); err != nil {
		slog.Error("letta could not persist a notification", "agent_id", a.AgentID(), "error", err)
	}
}

// finishOutputAfterExit preserves unfinished output before the worker retires this process.
func (a *Agent) finishOutputAfterExit(completion agent.MessageCompletion) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.outputEnded {
		return
	}
	a.outputEnded = true
	a.Mu.Lock()
	if a.transportErr != nil {
		completion = agent.MessageCompletionError
	}
	a.Mu.Unlock()
	if a.IsDiscardingOutput() {
		a.generation.DiscardMatching(func(string) bool { return true })
		a.heldErrors = nil
		return
	}
	ids := []string{""}
	for id, child := range a.children {
		if child.registered && !child.closed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if target, ok := a.outputTarget(id); ok {
			a.flushGenerationFor(id, completion)
			a.finishOpenCompaction(target, id, completion)
			a.flushHeldErrors(target, id, "")
			a.retryForTarget(id).Reset()
			a.finishToolOutputScope(id, target)
		}
	}
	a.setTurnActive(false)
}

// onNativeToolCalls gives each call in one native message its own span and row.
func (a *Agent) onNativeToolCalls(payload []byte, delta *lettaDelta, target agent.ProviderServices, subagentID string) {
	calls := delta.ToolCalls
	if len(calls) == 0 && delta.ToolCall != nil {
		calls = []lettaNativeToolCall{*delta.ToolCall}
	}
	var frame map[string]json.RawMessage
	if len(calls) > 1 {
		if err := json.Unmarshal(payload, &frame); err != nil {
			return
		}
		delete(frame, contracts.LettaDeltaFieldToolCall)
	}
	for _, call := range calls {
		if call.CallID == "" || call.Name == "" {
			continue
		}
		row := payload
		if len(calls) > 1 {
			one, err := json.Marshal([]lettaNativeToolCall{call})
			if err != nil {
				return
			}
			frame[contracts.LettaDeltaFieldToolCalls] = one
			row, err = json.Marshal(frame)
			if err != nil {
				return
			}
		}
		a.onToolStart(row, &lettaDelta{ToolCallID: call.CallID, ToolName: call.Name, RunID: delta.RunID}, target, subagentID)
	}
}

// deltaText joins the text blocks of a delta content payload.
func deltaText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	out := ""
	for _, b := range blocks {
		out += b.Text
	}
	return out
}

// onAssistantDelta accumulates streamed assistant text.
func (a *Agent) onAssistantDelta(delta *lettaDelta, subagentID string) {
	text := deltaText(delta.Content)
	if text == "" {
		return
	}
	// Keep reasoning open until the model commits a tool or turn boundary, so a retry can discard the whole attempt.
	if target, ok := a.outputTarget(subagentID); ok {
		a.retryForTarget(subagentID).Resolve(target)
	}
	a.generation.Append(lettaGenerationScope(subagentID), agent.AssembledMessageKindText, text, providerkit.JoinVerbatim)
}

// onReasoningDelta accumulates streamed thinking text.
func (a *Agent) onReasoningDelta(delta *lettaDelta, subagentID string) {
	text := delta.Reasoning
	if text == "" {
		text = deltaText(delta.Content)
	}
	if text == "" {
		return
	}
	if target, ok := a.outputTarget(subagentID); ok {
		a.retryForTarget(subagentID).Resolve(target)
	}
	a.generation.Append(lettaGenerationScope(subagentID)+":reasoning", agent.AssembledMessageKindReasoning, text, providerkit.JoinVerbatim)
}

// onToolStart opens a tool span.
func (a *Agent) onToolStart(payload []byte, delta *lettaDelta, target agent.ProviderServices, subagentID string) {
	if delta.ToolCallID == "" || delta.ToolName == "" {
		return
	}
	a.retryForTarget(subagentID).Resolve(target)
	a.flushGenerationFor(subagentID, agent.MessageCompletionComplete)
	spanID := "letta-tool-" + delta.ToolCallID
	toolKey := lettaToolKey(subagentID, delta.ToolCallID)
	a.Mu.Lock()
	if a.tools == nil {
		a.tools = make(map[string]*lettaTool)
	}
	previous := a.tools[toolKey]
	a.tools[toolKey] = &lettaTool{
		id:         delta.ToolCallID,
		name:       delta.ToolName,
		spanID:     spanID,
		runID:      delta.RunID,
		subagentID: subagentID,
	}
	if subagentID == "" {
		if a.turnToolCallIDs == nil {
			a.turnToolCallIDs = make(map[string]struct{})
		}
		a.turnToolCallIDs[delta.ToolCallID] = struct{}{}
	}
	a.Mu.Unlock()
	if previous != nil && previous.runID != delta.RunID {
		target.ReportProgress(agent.ResetOutputProgress(spanID))
	}
	target.OpenSpan(spanID, "")
	target.SetSpanType(spanID, delta.ToolName)
	a.persistRowTo(target, payload, agent.SpanInfo{SpanID: spanID, SpanType: delta.ToolName})
}

// onToolReturn stores native progress or an actual result.
func (a *Agent) onToolReturn(payload []byte, delta *lettaDelta, target agent.ProviderServices, subagentID string) {
	if isLettaToolProgress(delta) {
		a.onToolProgress(payload, delta, target, subagentID)
		return
	}
	_, present, valid := lettaReturnedData(delta)
	a.Mu.Lock()
	toolKey := lettaToolKey(subagentID, delta.ToolCallID)
	tool := a.tools[toolKey]
	owned := tool != nil && tool.runID == delta.RunID
	if owned && present && valid {
		delete(a.tools, toolKey)
	}
	a.Mu.Unlock()
	// A stale or malformed result stays native data without closing a newer call.
	if !present || !valid || (tool != nil && !owned) || delta.ToolCallID == "" {
		a.persistRowTo(target, payload, agent.SpanInfo{})
		return
	}
	spanID := "letta-tool-" + delta.ToolCallID
	if tool != nil {
		spanID = tool.spanID
	}
	target.ReportProgress(agent.CompleteOutputProgress(spanID))
	a.persistRowTo(target, payload, agent.SpanInfo{SpanID: spanID, Closing: true})
	target.CloseSpan(spanID)
	// A subagent runs headless, so no client answers its questions.
	if subagentID == "" {
		a.postQuestionRequest(tool, delta)
	}
}

// lettaTurnFinished is the part of a `turn_finished` frame that states how the
// turn ended. The tag is pinned to the `deltaFields` table of the contract.
type lettaTurnFinished struct {
	StopReason string `json:"stop_reason"`
	Error      string `json:"error"`
	TurnID     string `json:"turn_id"`
}

// lettaTurnCompletion maps the native stop reason of a finished turn onto the
// completion that the worker records with the turn end. Letta Code ends a turn
// that `abort_message` stopped with `cancelled`, and a turn whose model request
// failed with `error` or `llm_api_error`. Every other stop reason (`end_turn`,
// `max_steps`, and any that a later release adds) describes a turn that ran to
// its own end.
func lettaTurnCompletion(stopReason string) agent.MessageCompletion {
	switch stopReason {
	case contracts.LettaStopReasonCancelled:
		return agent.MessageCompletionInterrupted
	case contracts.LettaStopReasonError, contracts.LettaStopReasonLLMAPIError:
		return agent.MessageCompletionError
	default:
		return agent.MessageCompletionComplete
	}
}

// onTurnFinished ends the turn. PersistTurnEnd runs before the clear.
func (a *Agent) onTurnFinished(payload []byte) {
	var finished lettaTurnFinished
	if err := json.Unmarshal(payload, &finished); err != nil {
		slog.Debug("letta could not decode all turn-end fields", "agent_id", a.AgentID(), "error", err)
	}
	if finished.TurnID != "" {
		if slices.Contains(a.finishedTurnIDs, finished.TurnID) {
			return
		}
		a.finishedTurnIDs = append(a.finishedTurnIDs, finished.TurnID)
		if len(a.finishedTurnIDs) > lettaFinishedTurnLimit {
			a.finishedTurnIDs = slices.Delete(a.finishedTurnIDs, 0, len(a.finishedTurnIDs)-lettaFinishedTurnLimit)
		}
	}
	a.retryForTarget("").Reset()
	a.Mu.Lock()
	toolUses := len(a.turnToolCallIDs)
	a.turnToolCallIDs = nil
	a.Mu.Unlock()
	completion := lettaTurnCompletion(finished.StopReason)
	a.flushGenerationFor("", completion)
	a.finishOpenCompaction(a.sink, "", completion)
	a.flushHeldErrors(a.sink, "", finished.Error)
	content := agent.MessageContent{Original: payload, Completion: completion}
	if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(content, toolUses), agent.SpanInfo{}); err != nil {
		slog.Debug("letta: persist turn end failed", "agent_id", a.AgentID(), "error", err)
	}
	a.finishToolOutputScope("", a.sink)
	a.withdrawPermissions()
	a.setTurnActive(false)
}

// flushGenerationFor stores the reasoning and text completed so far in one transcript.
func (a *Agent) flushGenerationFor(subagentID string, completion agent.MessageCompletion) {
	a.flushGenerationScope(subagentID, true, completion)
	a.flushGenerationScope(subagentID, false, completion)
}

func (a *Agent) flushGenerationScope(subagentID string, reasoning bool, completion agent.MessageCompletion) {
	target, ok := a.outputTarget(subagentID)
	if !ok {
		return
	}
	scope := lettaGenerationScope(subagentID)
	if reasoning {
		scope += ":reasoning"
	}
	raw, ready, err := a.generation.Finish(scope, completion)
	if err != nil {
		slog.Error("letta could not finish generated output", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !ready {
		return
	}
	a.persistRowTo(target, raw, agent.SpanInfo{})
	if subagentID != "" && !reasoning {
		var message struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &message) == nil {
			a.children[subagentID].lastText = message.Text
		}
	}
}

// onLoopStatus is the busy signal. Only the listed states move the turn flag.
func (a *Agent) onLoopStatus(payload []byte) {
	var n struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(payload, &n); err != nil {
		return
	}
	switch n.Status {
	case contracts.LettaLoopStatusSendingAPIRequest, contracts.LettaLoopStatusWaitingForAPIResponse,
		contracts.LettaLoopStatusProcessingAPIResponse:
		a.setTurnActive(true)
	case contracts.LettaLoopStatusWaitingOnInput:
		// This state precedes turn_finished, including after an abort. The final frame owns queue release.
	}
}
