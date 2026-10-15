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

const codexExecTimingOutputPattern = `\nWall time \d+(?:\.\d+)? seconds(?: \(code-mode [\d.]+ seconds; overhead -?[\d.]+ seconds\))?\nOutput:\n$`

var codexCompletedExecHeader = regexp.MustCompile(`^` + regexp.QuoteMeta(contracts.CodexExecOutputCompletedHeader) + codexExecTimingOutputPattern)

var codexEmptyYieldHeader = regexp.MustCompile(`^Script running with cell ID \S+` + codexExecTimingOutputPattern)

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
		if !codexEmptyPollJSON(block.Text) {
			return false
		}
	}
	return true
}

// Match encoding/json's nesting limit. Token traversal otherwise has no nesting limit.
const codexPollJSONDepthLimit = 10000

// Read exactly one printed JSON value. Stop when content makes suppression unsafe.
// Tokens preserve duplicate keys and avoid allocating a complete tree for rejected output.
func codexEmptyPollJSON(text string) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || !codexEmptyPollValue(decoder, token, 0) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

// codexEmptyPollValue accepts a receipt or a nonempty collection of empty successful receipts.
// A script can print receipts in arrays or under container keys, including receipt field keys.
// These values keep the execution pair:
//   - A string outside a recognized receipt or status field.
//   - A boolean or null.
//   - A number outside a recognized receipt field or an i/index label.
//   - A rejected record.
//   - A duplicate object key.
func codexEmptyPollValue(decoder *json.Decoder, token json.Token, depth int) bool {
	if depth >= codexPollJSONDepthLimit {
		return false
	}
	switch token {
	case json.Delim('['):
		if !decoder.More() {
			return false
		}
		for decoder.More() {
			item, err := decoder.Token()
			if err != nil || !codexEmptyPollValue(decoder, item, depth+1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	case json.Delim('{'):
		return codexEmptyPollObject(decoder, depth)
	default:
		return false
	}
}

// Classify each object as a receipt or a container while reading each field once.
// Nested receipts can use any key except status, which identifies a settled record.
// A scalar receipt field cannot become a container label when receipt validation fails.
func codexEmptyPollObject(decoder *json.Decoder, depth int) bool {
	fields := make(map[string]bool)
	receipt, container := true, true
	foundPoll, valuePoll := false, false
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] {
			return false
		}
		fields[key] = true
		value, err := decoder.Token()
		if err != nil {
			return false
		}
		if _, nested := value.(json.Delim); nested {
			if !codexEmptyPollValue(decoder, value, depth+1) {
				return false
			}
			receipt = false
			container = container && key != "status"
			foundPoll = true
			if key == "value" {
				valuePoll = true
			}
		} else {
			receipt = receipt && codexEmptyPollReceiptField(key, value)
			container = container && codexEmptyPollLabelField(key, value)
		}
		if !receipt && !container {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	if receipt {
		return fields["chunk_id"] && fields["wall_time_seconds"] && fields["original_token_count"] && fields["output"] &&
			(fields["session_id"] || fields["exit_code"])
	}
	return container && foundPoll && (!fields["status"] || valuePoll)
}

// Only recognized labels can accompany a nested receipt. Other scalars can carry results or errors.
func codexEmptyPollLabelField(key string, value json.Token) bool {
	switch key {
	case "status":
		return value == "fulfilled"
	case "i", "index":
		_, ok := codexJSONInteger(value)
		return ok
	default:
		return false
	}
}

// A running receipt supplies a positive session ID. An exited receipt supplies exit code zero.
// Codex sends the exit through commandExecution also, so the typed row already shows it.
func codexEmptyPollReceiptField(key string, value json.Token) bool {
	switch key {
	case "chunk_id":
		chunkID, ok := value.(string)
		return ok && chunkID != ""
	case "output":
		return value == ""
	case "wall_time_seconds":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		wallTime, err := number.Float64()
		return err == nil && wallTime >= 0
	case "session_id":
		sessionID, ok := codexJSONInteger(value)
		return ok && sessionID > 0
	case "original_token_count", "exit_code":
		number, ok := codexJSONInteger(value)
		return ok && number == 0
	default:
		return false
	}
}

func codexJSONInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := number.Int64()
	return integer, err == nil
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
