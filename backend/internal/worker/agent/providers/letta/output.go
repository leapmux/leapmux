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

// lettaAttachmentLabel names the provider in an attachment refusal.
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

// lettaDelta is the payload of one stream_delta.
type lettaDelta struct {
	MessageType      string                `json:"message_type"`
	Content          json.RawMessage       `json:"content"`
	Reasoning        string                `json:"reasoning"`
	ToolCallID       string                `json:"tool_call_id"`
	ToolCalls        []lettaNativeToolCall `json:"tool_calls"`
	ToolCall         *lettaNativeToolCall  `json:"tool_call"`
	ToolName         string                `json:"tool_name"`
	ToolInput        json.RawMessage       `json:"tool_input"`
	ToolReturn       json.RawMessage       `json:"tool_return"`
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
	target, ok := a.outputTarget(subagentID)
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
		if subagentID == "" {
			a.rememberChildTaskReceipt(&delta)
		}
		a.onToolReturn(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindClientToolStart:
		a.onToolStart(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindToolCallMessage:
		a.onNativeToolCalls(payload, &delta, target, subagentID)
	case contracts.LettaDeltaKindClientToolEnd:
		a.persistRowTo(target, payload, agent.SpanInfo{})
	case contracts.LettaDeltaKindUserMessage:
		if subagentID == "" {
			a.observeChildTaskNotifications(deltaText(delta.Content))
		}
		if subagentID == "" || deltaText(delta.Content) != a.children[subagentID].prompt {
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
		a.onToolStart(row, &lettaDelta{ToolCallID: call.CallID, ToolName: call.Name}, target, subagentID)
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
	a.flushGenerationFor(subagentID)
	spanID := "letta-tool-" + delta.ToolCallID
	toolKey := lettaToolKey(subagentID, delta.ToolCallID)
	a.Mu.Lock()
	if a.tools == nil {
		a.tools = make(map[string]*lettaTool)
	}
	a.tools[toolKey] = &lettaTool{
		id:     delta.ToolCallID,
		name:   delta.ToolName,
		spanID: spanID,
	}
	a.Mu.Unlock()
	target.OpenSpan(spanID, "")
	target.SetSpanType(spanID, delta.ToolName)
	a.persistRowTo(target, payload, agent.SpanInfo{SpanID: spanID, SpanType: delta.ToolName})
}

// onToolReturn closes a tool span.
func (a *Agent) onToolReturn(payload []byte, delta *lettaDelta, target agent.ProviderServices, subagentID string) {
	a.Mu.Lock()
	toolKey := lettaToolKey(subagentID, delta.ToolCallID)
	tool := a.tools[toolKey]
	delete(a.tools, toolKey)
	a.Mu.Unlock()
	spanID := "letta-tool-" + delta.ToolCallID
	if tool != nil {
		spanID = tool.spanID
	}
	target.CloseSpan(spanID)
	a.persistRowTo(target, payload, agent.SpanInfo{SpanID: spanID, Closing: true})
}

// onTurnFinished ends the turn. PersistTurnEnd runs before the clear.
func (a *Agent) onTurnFinished(payload []byte) {
	a.flushGenerationFor("")
	if err := a.sink.PersistTurnEnd(agent.MessageContent{Original: payload}, agent.SpanInfo{}); err != nil {
		slog.Debug("letta: persist turn end failed", "agent_id", a.AgentID(), "error", err)
	}
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

// onLoopStatus is the busy signal. Only the named states move the turn flag.
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
