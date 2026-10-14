package codex

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const codexRawExecutionLimit = 128

type codexRawExecutionKey struct {
	threadID string
	turnID   string
	callID   string
}

type codexRawExecutionCall struct {
	request       agent.MessageContent
	output        agent.MessageContent
	owner         agent.TranscriptOwner
	sink          agent.ProviderServices
	suppressed    bool
	completed     bool
	requestStored bool
	color         int32
	order         uint64
}

// Buffer native requests until output determines whether another tool row overlaps them.
func (a *Agent) handleCodexRawExecution(raw, params json.RawMessage) {
	var envelope contracts.CodexRawExecutionParams
	var item contracts.CodexRawExecutionItem
	if json.Unmarshal(params, &envelope) != nil || envelope.ThreadID == "" || envelope.TurnID == "" ||
		json.Unmarshal(envelope.Item, &item) != nil || item.CallID == "" ||
		(item.Type != contracts.CodexRawItemCustomToolCall && item.Type != contracts.CodexRawItemCustomToolCallOutput) {
		return
	}
	if item.Type == contracts.CodexRawItemCustomToolCall &&
		(item.Name != contracts.CodexRawToolExec || item.Input == nil) {
		return
	}
	if (item.Namespace != "" && item.Namespace != contracts.CodexRawNamespaceFunctions) ||
		(item.Type == contracts.CodexRawItemCustomToolCallOutput && item.Name != "" && item.Name != contracts.CodexRawToolExec) {
		return
	}
	sink := a.sink
	mainThread := a.isMainThreadID(envelope.ThreadID)
	if !mainThread {
		route, ok := a.ensureCodexChildRoute(envelope.ThreadID)
		if !ok {
			a.enqueuePendingCodexChildEvent(envelope.ThreadID, codexPendingChildEvent{
				kind: codexPendingRawExecution, raw: append(json.RawMessage(nil), raw...), params: append(json.RawMessage(nil), params...),
			})
			return
		}
		a.replayPendingCodexChildEvents(envelope.ThreadID, route)
		sink = route.childSink
	}
	a.Mu.Lock()
	turnID := a.turnID
	if !mainThread {
		if state := a.collabChildren[envelope.ThreadID]; state != nil {
			turnID = state.turnID
		} else {
			turnID = ""
		}
	}
	key := codexRawExecutionKey{threadID: envelope.ThreadID, turnID: envelope.TurnID, callID: item.CallID}
	a.Mu.Unlock()
	call := a.rawExecutionCalls[key]
	if turnID == "" || turnID != envelope.TurnID {
		return
	}
	if item.Type == contracts.CodexRawItemCustomToolCall {
		if call != nil {
			return
		}
		if a.rawExecutionCalls == nil {
			a.rawExecutionCalls = make(map[codexRawExecutionKey]*codexRawExecutionCall)
		}
		if len(a.rawExecutionCalls) >= codexRawExecutionLimit {
			var oldest codexRawExecutionKey
			var oldestCall *codexRawExecutionCall
			for candidate, tracked := range a.rawExecutionCalls {
				if oldestCall == nil || tracked.order < oldestCall.order {
					oldest, oldestCall = candidate, tracked
				}
			}
			delete(a.rawExecutionCalls, oldest)
		}
		// Capture the owner now. Capture row geometry only when the pair reaches storage.
		captured := sink.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
		a.rawExecutionCalls[key] = &codexRawExecutionCall{
			request: agent.MessageContent{Original: append([]byte(nil), raw...), AgentSessionID: captured.AgentSessionID},
			sink:    sink, order: a.rawExecutionOrder,
		}
		if captured.Publication != nil {
			a.rawExecutionCalls[key].owner = captured.Publication.Owner()
		}
		a.rawExecutionOrder++
		return
	}

	output := bytes.TrimSpace(item.Output)
	if call == nil || call.completed || len(output) == 0 || (output[0] != '"' && output[0] != '[') {
		return
	}
	if call.output.Original == nil {
		if call.suppressed || codexSuppressRawExecutionOutput(item.Output) {
			call.release()
			return
		}
		call.output = agent.MessageContent{Original: append([]byte(nil), raw...), AgentSessionID: call.request.AgentSessionID}
	}
	a.persistCodexRawExecution(key, call)
}

var codexCompletedExecHeader = regexp.MustCompile(`^` + regexp.QuoteMeta(contracts.CodexExecOutputCompletedHeader) + `\nWall time \d+(?:\.\d+)? seconds(?: \(code-mode [\d.]+ seconds; overhead -?[\d.]+ seconds\))?\nOutput:\n$`)

var codexEmptyYieldHeader = regexp.MustCompile(`^Script running with cell ID \S+\nWall time \d+(?:\.\d+)? seconds(?: \(code-mode [\d.]+ seconds; overhead -?[\d.]+ seconds\))?\nOutput:\n$`)

// Suppress empty yielded status and successful output made entirely of empty shell-poll receipts.
// Unknown receipt fields can carry errors or other content, so they prevent suppression.
func codexSuppressRawExecutionOutput(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '"' {
		var text string
		return json.Unmarshal(raw, &text) == nil && codexEmptyYieldHeader.MatchString(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 || blocks[0].Type != contracts.CodexExecOutputTextContent {
		return false
	}
	if codexEmptyYieldHeader.MatchString(blocks[0].Text) {
		for _, block := range blocks[1:] {
			if block.Type != contracts.CodexExecOutputTextContent || block.Text != "" {
				return false
			}
		}
		return true
	}
	if len(blocks) < 2 || !codexCompletedExecHeader.MatchString(blocks[0].Text) {
		return false
	}
	for _, block := range blocks[1:] {
		if block.Type != contracts.CodexExecOutputTextContent {
			return false
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(block.Text), &fields) != nil {
			return false
		}
		for field := range fields {
			switch field {
			case "chunk_id", "wall_time_seconds", "session_id", "original_token_count", "output", "exit_code":
			default:
				return false
			}
		}
		var receipt struct {
			ChunkID   string   `json:"chunk_id"`
			WallTime  *float64 `json:"wall_time_seconds"`
			SessionID *int64   `json:"session_id"`
			Tokens    *int64   `json:"original_token_count"`
			Output    *string  `json:"output"`
			ExitCode  *int64   `json:"exit_code"`
		}
		if json.Unmarshal([]byte(block.Text), &receipt) != nil || receipt.ChunkID == "" || receipt.WallTime == nil || *receipt.WallTime < 0 || receipt.SessionID == nil || *receipt.SessionID <= 0 || receipt.Tokens == nil || *receipt.Tokens != 0 || receipt.Output == nil || *receipt.Output != "" || (receipt.ExitCode != nil && *receipt.ExitCode != 0) {
			return false
		}
	}
	return true
}

// A completed identity remains until turn cleanup or eviction, without retained payloads.
func (call *codexRawExecutionCall) release() {
	call.completed = true
	call.request = agent.MessageContent{}
	call.output = agent.MessageContent{}
	call.owner = nil
	call.sink = nil
}

func (a *Agent) persistCodexRawExecution(key codexRawExecutionKey, call *codexRawExecutionCall) {
	if (call.owner != nil && !call.owner.IsCurrent()) || a.IsDiscardingOutput() {
		call.release()
		return
	}
	sink := call.sink
	span := agent.SpanInfo{SpanID: key.callID, SpanType: contracts.CodexRawToolExec}
	// A failed write must release its reservation or open span before another event arrives.
	defer sink.CloseSpan(key.callID)
	if !call.requestStored {
		if call.request.Publication == nil {
			call.color = sink.ReserveSpanColor(key.callID, "")
			span.SpanColor = call.color
			call.request = sink.CaptureMessage(call.request, span)
		}
		span.SpanColor = call.color
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, call.request, span); err != nil {
			// The deferred close releases the reservation. Recapture geometry before the next request write.
			call.request.Publication = nil
			slog.Error("Cannot persist the Codex execution request", "agent_id", a.AgentID(), "call_id", key.callID, "error", err)
			return
		}
		call.requestStored = true
	}
	sink.SetSpanType(key.callID, contracts.CodexRawToolExec)
	sink.OpenSpan(key.callID, "")
	span.Closing = true
	span.SpanColor = call.color
	if call.output.Publication == nil {
		call.output = sink.CaptureMessage(call.output, span)
	}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, call.output, span); err != nil {
		slog.Error("Cannot persist the Codex execution output", "agent_id", a.AgentID(), "call_id", key.callID, "error", err)
		return
	}
	call.release()
	sink.ReportProgress(agent.CompleteOutputProgress(key.callID))
	if a.isMainThreadID(key.threadID) {
		a.Mu.Lock()
		a.TurnToolUses++
		a.Mu.Unlock()
	}
}

// Codex omits parent execution IDs. Tool events suppress pending pairs within the same thread and turn.
func (a *Agent) observeCodexRawExecutionTool(event codexItemEvent) {
	switch event.itemType {
	case contracts.CodexItemTypeCommandExecution, contracts.CodexItemTypeFileChange,
		contracts.CodexItemTypeMcpToolCall, contracts.CodexItemTypeDynamicToolCall,
		contracts.CodexItemTypeImageGeneration, contracts.CodexItemTypeImageView,
		contracts.CodexItemTypeCollabAgentToolCall, contracts.CodexItemTypeWebSearch,
		contracts.CodexItemTypeSleep, contracts.CodexItemTypeEnteredReviewMode,
		contracts.CodexItemTypeExitedReviewMode, contracts.CodexItemTypeHookPrompt,
		contracts.CodexItemTypeFunctionCallOutput:
	default:
		return
	}
	if event.itemID == "" || event.threadID == "" || event.turnID == "" {
		return
	}
	for key, call := range a.rawExecutionCalls {
		if key.threadID == event.threadID && key.turnID == event.turnID && !call.completed && call.output.Original == nil {
			call.suppressed = true
		}
	}
}

// Retry eligible pairs once before the turn ends. Requests without output stay absent from history.
func (a *Agent) finishCodexRawExecutions(threadID string) {
	var keys []codexRawExecutionKey
	for key := range a.rawExecutionCalls {
		if key.threadID == threadID {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return a.rawExecutionCalls[keys[i]].order < a.rawExecutionCalls[keys[j]].order })
	for _, key := range keys {
		call := a.rawExecutionCalls[key]
		if !call.completed && call.output.Original != nil {
			a.persistCodexRawExecution(key, call)
		}
		delete(a.rawExecutionCalls, key)
	}
}

// A new turn drops stale state even when the preceding turn sent no completion.
func (a *Agent) clearCodexRawExecutionsBeforeTurn(threadID, turnID string) {
	for key := range a.rawExecutionCalls {
		if key.threadID == threadID && key.turnID != turnID {
			delete(a.rawExecutionCalls, key)
		}
	}
}
