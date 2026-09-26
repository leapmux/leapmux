package letta

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Letta Code's conversation output.
//
// Every stream_delta carries a `message_type` and the runtime scope. The
// persisted rows are the server's own payload objects, verbatim, so the browser
// plugin dispatches on the same `message_type`. The only rows the worker builds
// are the assembled text and thinking.

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
	MessageType string          `json:"message_type"`
	SubagentID  string          `json:"subagent_id"`
	Content     json.RawMessage `json:"content"`
	ToolCallID  string          `json:"tool_call_id"`
	ToolName    string          `json:"tool_name"`
	ToolInput   json.RawMessage `json:"tool_input"`
	ToolReturn  json.RawMessage `json:"tool_return"`
}

// onStreamDelta dispatches one stream_delta payload.
func (a *Agent) onStreamDelta(payload []byte) {
	var delta lettaDelta
	if err := json.Unmarshal(payload, &delta); err != nil {
		slog.Debug("letta: bad stream delta", "agent_id", a.AgentID(), "error", err)
		return
	}

	switch delta.MessageType {
	case contracts.LettaDeltaKindAssistantMessage:
		a.onAssistantDelta(payload, &delta)
	case contracts.LettaDeltaKindReasoningMessage:
		a.onReasoningDelta(payload, &delta)
	case contracts.LettaDeltaKindToolReturnMessage:
		a.onToolReturn(payload, &delta)
	case contracts.LettaDeltaKindClientToolStart:
		a.onToolStart(payload, &delta)
	case contracts.LettaDeltaKindClientToolEnd:
		a.persistRow(payload, agent.SpanInfo{})
	case contracts.LettaDeltaKindUserMessage:
		a.persistRow(payload, agent.SpanInfo{})
	case contracts.LettaDeltaKindRetry, contracts.LettaDeltaKindLoopError:
		// A retry and a loop error are events the reader may want to see.
		a.persistNotification(payload)
	case contracts.LettaDeltaKindStopReason, contracts.LettaDeltaKindUsageStatistics,
		contracts.LettaDeltaKindStatus:
		// Protocol state. `turn_finished` already states the stop reason as a
		// result divider, and the token counts draw nothing.
	default:
		// An unknown delta kind must move nothing.
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
func (a *Agent) onAssistantDelta(payload []byte, delta *lettaDelta) {
	text := deltaText(delta.Content)
	if text == "" {
		return
	}
	scope := delta.SubagentID
	if scope == "" {
		scope = "main"
	}
	a.generation.Append(scope, agent.AssembledMessageKindText, text, providerkit.JoinVerbatim)
}

// onReasoningDelta accumulates streamed thinking text.
func (a *Agent) onReasoningDelta(payload []byte, delta *lettaDelta) {
	text := deltaText(delta.Content)
	if text == "" {
		return
	}
	scope := delta.SubagentID + ":reasoning"
	if delta.SubagentID == "" {
		scope = "main:reasoning"
	}
	a.generation.Append(scope, agent.AssembledMessageKindReasoning, text, providerkit.JoinVerbatim)
}

// onToolStart opens a tool span.
func (a *Agent) onToolStart(payload []byte, delta *lettaDelta) {
	spanID := "letta-tool-" + delta.ToolCallID
	a.Mu.Lock()
	if a.tools == nil {
		a.tools = make(map[string]*lettaTool)
	}
	a.tools[delta.ToolCallID] = &lettaTool{
		id:     delta.ToolCallID,
		name:   delta.ToolName,
		spanID: spanID,
	}
	a.Mu.Unlock()
	a.sink.OpenSpan(spanID, "")
	a.sink.SetSpanType(spanID, delta.ToolName)
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID, SpanType: delta.ToolName})
}

// onToolReturn closes a tool span.
func (a *Agent) onToolReturn(payload []byte, delta *lettaDelta) {
	a.Mu.Lock()
	tool := a.tools[delta.ToolCallID]
	delete(a.tools, delta.ToolCallID)
	a.Mu.Unlock()
	spanID := "letta-tool-" + delta.ToolCallID
	if tool != nil {
		spanID = tool.spanID
	}
	a.sink.CloseSpan(spanID)
	a.persistRow(payload, agent.SpanInfo{SpanID: spanID, Closing: true})
}

// onTurnFinished ends the turn. PersistTurnEnd runs before the clear.
func (a *Agent) onTurnFinished(payload []byte) {
	a.muFlushGeneration()
	if err := a.sink.PersistTurnEnd(agent.MessageContent{Original: payload}, agent.SpanInfo{}); err != nil {
		slog.Debug("letta: persist turn end failed", "agent_id", a.AgentID(), "error", err)
	}
	a.disarmTurn()
}

// muFlushGeneration flushes every open generation buffer into a transcript row.
func (a *Agent) muFlushGeneration() {
	for _, scope := range []string{"main", "main:reasoning"} {
		raw, ok, err := a.generation.Finish(scope, agent.MessageCompletionComplete)
		if err != nil || !ok {
			continue
		}
		a.persistRow(raw, agent.SpanInfo{})
	}
}

// onSubagentState records a child lifecycle snapshot. The whole frame is the
// row: the browser reads `subagents` from it beside the message kind.
func (a *Agent) onSubagentState(line []byte, _ json.RawMessage) {
	a.persistNotification(line)
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
