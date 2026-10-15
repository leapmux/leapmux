package codex

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"

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
// After a completed header, each printed block must hold an empty poll (see codexEmptyPollValue).
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
		value, ok := codexDecodePrintedJSON(block.Text)
		if !ok || !codexEmptyPollValue(value) {
			return false
		}
	}
	return true
}

// codexPollReceiptFields lists the fields of the result that write_stdin gives a script.
// Codex omits each field that has no value, so one receipt holds a subset of them.
var codexPollReceiptFields = map[string]bool{
	"chunk_id": true, "wall_time_seconds": true, "session_id": true, "original_token_count": true, "output": true, "exit_code": true,
}

// Decode one printed block as exactly one JSON value. UseNumber keeps an integer separate from a fraction.
// Trailing text after the value keeps the pair.
func codexDecodePrintedJSON(text string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return value, true
}

// codexEmptyPollValue reports whether a printed value holds at least one empty successful receipt and nothing else
// that can carry content. A script chooses how it prints each receipt:
//   - bare, as text(r) prints it
//   - in an array, as text(JSON.stringify(results)) prints it
//   - in a fulfilled Promise.allSettled record, beside integer labels, as text({i, ...settled}) prints it
//   - under any key of a container, as text({i, result: settled}) prints it
//
// A string, a boolean, null, a fraction, and a rejected record keep the pair. A receipt keeps its own fields
// only, because an unknown field can carry an error: text({i, ...r}) keeps the pair.
func codexEmptyPollValue(value any) bool {
	switch value := value.(type) {
	case []any:
		if len(value) == 0 {
			return false
		}
		for _, item := range value {
			if !codexEmptyPollValue(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for key := range value {
			if codexPollReceiptFields[key] {
				return codexEmptyPollReceipt(value)
			}
		}
		return codexEmptyPollContainer(value)
	default:
		return false
	}
}

// A container holds no receipt field. Each field holds an empty poll or an integer label, and a status field marks
// a fulfilled Promise.allSettled record. The status case does not examine the value field: the loop examines each
// field once, so a value that nests settled records deeply costs linear time.
func codexEmptyPollContainer(fields map[string]any) bool {
	_, settled := fields["status"]
	found := false
	for key, field := range fields {
		switch {
		case key == "status":
			// A rejected record holds a reason instead of a value, and the reason carries the error.
			if status, _ := field.(string); status != "fulfilled" {
				return false
			}
			if _, ok := fields["value"]; !ok {
				return false
			}
		case codexEmptyPollValue(field):
			found = true
		case settled && key == "value":
			return false
		case codexIsJSONInteger(field):
			// A label, such as the index of a result.
		default:
			return false
		}
	}
	return found
}

// codexEmptyPollReceipt reports whether a receipt states a successful poll that read no output.
// A running session states its session ID. An exited session states exit code 0 and no session ID. Codex sends the
// exit as the commandExecution completion when the process exits, so that typed row already shows it.
func codexEmptyPollReceipt(fields map[string]any) bool {
	for key := range fields {
		if !codexPollReceiptFields[key] {
			return false
		}
	}
	chunkID, _ := fields["chunk_id"].(string)
	wallTime, wallTimeOK := codexJSONFloat(fields["wall_time_seconds"])
	tokens, tokensOK := codexJSONInteger(fields["original_token_count"])
	output, outputOK := fields["output"].(string)
	if chunkID == "" || !wallTimeOK || wallTime < 0 || !tokensOK || tokens != 0 || !outputOK || output != "" {
		return false
	}
	exitField, exited := fields["exit_code"]
	if exited {
		if exitCode, ok := codexJSONInteger(exitField); !ok || exitCode != 0 {
			return false
		}
	}
	sessionField, running := fields["session_id"]
	if !running {
		return exited
	}
	sessionID, ok := codexJSONInteger(sessionField)
	return ok && sessionID > 0
}

func codexJSONInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := number.Int64()
	return integer, err == nil
}

func codexIsJSONInteger(value any) bool {
	_, ok := codexJSONInteger(value)
	return ok
}

func codexJSONFloat(value any) (float64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	float, err := number.Float64()
	return float, err == nil
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
