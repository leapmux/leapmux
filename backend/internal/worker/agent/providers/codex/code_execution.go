package codex

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type codexRawExecutionKey struct {
	threadID string
	turnID   string
	callID   string
}

type codexRawExecutionCall struct {
	request   json.RawMessage
	completed bool
	order     uint64
}

// Native exec has no typed command item. Retain its original request and paired output.
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
	call := a.rawExecutionCalls[key]
	a.Mu.Unlock()
	if turnID == "" || turnID != envelope.TurnID {
		return
	}
	if item.Type == contracts.CodexRawItemCustomToolCall {
		if call != nil {
			return
		}
		color := sink.ReserveSpanColor(item.CallID, "")
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{
			SpanID: item.CallID, SpanType: contracts.CodexRawToolExec, SpanColor: color,
		}); err != nil {
			slog.Error("persist Codex native exec request", "agent_id", a.AgentID(), "call_id", item.CallID, "error", err)
			return
		}
		sink.SetSpanType(item.CallID, contracts.CodexRawToolExec)
		sink.OpenSpan(item.CallID, "")
		a.Mu.Lock()
		if a.rawExecutionCalls == nil {
			a.rawExecutionCalls = make(map[codexRawExecutionKey]*codexRawExecutionCall)
		}
		a.rawExecutionCalls[key] = &codexRawExecutionCall{request: append(json.RawMessage(nil), raw...), order: a.rawExecutionOrder}
		a.rawExecutionOrder++
		a.Mu.Unlock()
		return
	}
	output := bytes.TrimSpace(item.Output)
	if call == nil || call.completed || len(output) == 0 || (output[0] != '"' && output[0] != '[') {
		return
	}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{
		SpanID: item.CallID, SpanType: contracts.CodexRawToolExec, Closing: true,
	}); err != nil {
		slog.Error("persist Codex native exec result", "agent_id", a.AgentID(), "call_id", item.CallID, "error", err)
		return
	}
	sink.CloseSpan(item.CallID)
	sink.ReportProgress(agent.CompleteOutputProgress(item.CallID))
	a.Mu.Lock()
	call.completed = true
	if mainThread {
		a.TurnToolUses++
	}
	a.Mu.Unlock()
}

// Close unfinished raw calls with their original request and the turn's actual completion.
func (a *Agent) finishCodexRawExecutions(threadID string, completion agent.MessageCompletion) int {
	a.Mu.Lock()
	type pending struct {
		key  codexRawExecutionKey
		call *codexRawExecutionCall
	}
	var calls []pending
	for key, call := range a.rawExecutionCalls {
		if key.threadID != threadID {
			continue
		}
		if call != nil && !call.completed {
			calls = append(calls, pending{key: key, call: call})
		}
		delete(a.rawExecutionCalls, key)
	}
	a.Mu.Unlock()
	if a.IsDiscardingOutput() {
		return 0
	}
	if completion == agent.MessageCompletionComplete {
		completion = agent.MessageCompletionInterrupted
	}
	sort.Slice(calls, func(left, right int) bool { return calls[left].call.order < calls[right].call.order })
	sink := a.sink
	if !a.isMainThreadID(threadID) {
		route, ok := a.lookupCodexChildRoute(threadID)
		if !ok {
			return 0
		}
		sink = route.childSink
	}
	for _, call := range calls {
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original: call.call.request, Completion: completion,
		}, agent.SpanInfo{SpanID: call.key.callID, SpanType: contracts.CodexRawToolExec, Closing: true}); err != nil {
			slog.Error("retain interrupted Codex exec request", "agent_id", a.AgentID(), "call_id", call.key.callID, "error", err)
		}
		sink.CloseSpan(call.key.callID)
		sink.ReportProgress(agent.CompleteOutputProgress(call.key.callID))
	}
	return len(calls)
}
