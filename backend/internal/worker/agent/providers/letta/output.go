package letta

import (
	"encoding/json"
	"log/slog"

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
}

type lettaNativeToolCall struct {
	CallID    string          `json:"tool_call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// onStreamDelta dispatches one stream_delta payload.
func (a *Agent) onStreamDelta(payload []byte, subagentID string) {
	var delta lettaDelta
	if err := json.Unmarshal(payload, &delta); err != nil {
		slog.Debug("letta: bad stream delta", "agent_id", a.AgentID(), "error", err)
		return
	}
	target, ok := a.toolOutputTarget(subagentID, delta.MessageType)
	if !ok {
		slog.Debug("letta: unknown child delta", "agent_id", a.AgentID(), "subagent_id", subagentID)
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
		// Letta Code echoes a message that it queued when the message starts.
		// LeapMux stored each message that it sent: the reader's text as a user
		// row, and the answer to a question as a control response. An echo that
		// states a LeapMux id therefore draws no second row. It is also no
		// notification of a child, because the message is the reader's own text.
		if isLeapMuxClientMessageID(delta.OTID) {
			return
		}
		text := deltaText(delta.Content)
		if subagentID == "" {
			a.observeChildTaskNotifications(text)
			if isLettaQuestionNotification(text) {
				return
			}
		}
		if subagentID == "" || text != a.children[subagentID].prompt {
			a.persistRowTo(target, payload, agent.SpanInfo{})
		}
	case contracts.LettaDeltaKindRetry, contracts.LettaDeltaKindLoopError:
		// A retry and a loop error are events the reader may want to see.
		if _, err := target.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, payload); err != nil {
			slog.Debug("letta: persist notification failed", "agent_id", a.AgentID(), "error", err)
		}
	case contracts.LettaDeltaKindUsageStatistics:
		publishLettaUsage(target, &delta)
	case contracts.LettaDeltaKindStopReason, contracts.LettaDeltaKindStatus:
		// Protocol state. `turn_finished` already states the stop reason as a
		// result divider.
	default:
		// An unknown delta kind must move nothing.
	}
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
	a.flushGenerationScope(subagentID, true)
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
	a.generation.Append(lettaGenerationScope(subagentID)+":reasoning", agent.AssembledMessageKindReasoning, text, providerkit.JoinVerbatim)
}

// onToolStart opens a tool span.
func (a *Agent) onToolStart(payload []byte, delta *lettaDelta, target agent.ProviderServices, subagentID string) {
	if delta.ToolCallID == "" || delta.ToolName == "" {
		return
	}
	a.flushGenerationFor(subagentID)
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
	target.CloseSpan(spanID)
	a.persistRowTo(target, payload, agent.SpanInfo{SpanID: spanID, Closing: true})
	// A subagent runs headless, so no client answers its questions.
	if subagentID == "" {
		a.postQuestionRequest(tool, delta)
	}
}

// lettaTurnFinished is the part of a `turn_finished` frame that states how the
// turn ended. The tag is pinned to the `deltaFields` table of the contract.
type lettaTurnFinished struct {
	StopReason string `json:"stop_reason"`
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
	a.flushGenerationFor("")
	a.Mu.Lock()
	toolUses := len(a.turnToolCallIDs)
	a.turnToolCallIDs = nil
	a.Mu.Unlock()
	var finished lettaTurnFinished
	if err := json.Unmarshal(payload, &finished); err != nil {
		// A stop reason that is not text reads as no stop reason. The turn still
		// ends, as a turn that ran to its own end.
		slog.Debug("letta: unreadable stop reason", "agent_id", a.AgentID(), "error", err)
	}
	content := agent.MessageContent{Original: payload, Completion: lettaTurnCompletion(finished.StopReason)}
	if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(content, toolUses), agent.SpanInfo{}); err != nil {
		slog.Debug("letta: persist turn end failed", "agent_id", a.AgentID(), "error", err)
	}
	a.finishToolOutputScope("", a.sink)
	a.disarmTurn()
}

// flushGenerationFor stores the reasoning and text completed so far in one transcript.
func (a *Agent) flushGenerationFor(subagentID string) {
	a.flushGenerationScope(subagentID, true)
	a.flushGenerationScope(subagentID, false)
}

func (a *Agent) flushGenerationScope(subagentID string, reasoning bool) {
	target, ok := a.outputTarget(subagentID)
	if !ok {
		return
	}
	scope := lettaGenerationScope(subagentID)
	if reasoning {
		scope += ":reasoning"
	}
	raw, ready, err := a.generation.Finish(scope, agent.MessageCompletionComplete)
	if err != nil || !ready {
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
		a.armTurn()
	case contracts.LettaLoopStatusWaitingOnInput:
		a.disarmTurn()
	}
}
