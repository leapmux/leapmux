package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// Raw item types and the code-mode tool that only Go reads. The contract
// tables hold the exec vocabulary that the browser reads also.
const (
	codexRawItemFunctionCall       = "function_call"
	codexRawItemFunctionCallOutput = "function_call_output"
	codexRawToolWait               = "wait"
)

type codexRawExecutionKey struct {
	threadID string
	turnID   string
	callID   string
}

// codexRawExecutionCall is one code-mode cell: the exec call that starts it,
// and the wait calls that read the rest of its output after it yields.
type codexRawExecutionCall struct {
	request   agent.MessageContent
	output    agent.MessageContent
	owner     agent.TranscriptOwner
	sink      agent.ProviderServices
	commands  map[codexStoredCommandResult]struct{}
	completed bool
	color     int32
	order     uint64
	// cellID identifies the cell after the exec output yields. It is empty
	// for a cell that ended in its exec output.
	cellID string
	// waits holds the call ID of each wait on the cell that has no output yet.
	waits map[string]struct{}
	// continuations holds the `output` value of each wait output, in order.
	// Each yield returns only the output since the yield before it.
	continuations []json.RawMessage
	// ended reports that an output stated a final status. The final status
	// decides the card once, and a later retry only writes it again.
	ended bool
}

// awaited reports whether the model waits on the cell now: before the exec
// output, and inside a wait. Codex omits the parent of a nested tool call, so
// a typed tool event covers the cell only in these intervals. A tool call that
// the model makes while the cell runs in the background is not the cell's.
func (call *codexRawExecutionCall) awaited() bool {
	return !call.completed && (call.output.Original == nil || len(call.waits) > 0)
}

// outputs returns every output of the cell in order, from its exec output.
func (call *codexRawExecutionCall) outputs(execOutput json.RawMessage) []json.RawMessage {
	return append([]json.RawMessage{execOutput}, call.continuations...)
}

// Buffer native requests until output determines whether another tool row overlaps them.
func (a *Agent) handleCodexRawExecution(raw, params json.RawMessage) {
	var envelope contracts.CodexRawExecutionParams
	var item contracts.CodexRawExecutionItem
	if json.Unmarshal(params, &envelope) != nil || envelope.ThreadID == "" || envelope.TurnID == "" ||
		json.Unmarshal(envelope.Item, &item) != nil || item.CallID == "" || !codexRawExecutionItemAccepted(item) {
		return
	}
	sink := a.sink
	mainThread := a.isMainThreadID(envelope.ThreadID)
	if !mainThread {
		route, ok := a.ensureCodexChildRoute(envelope.ThreadID)
		if !ok {
			// Any function_call_output can arrive, so the queue keeps only the
			// output of a wait call that it holds already.
			if item.Type != codexRawItemFunctionCallOutput || a.pendingCodexChildWait(envelope.ThreadID, item.CallID) {
				a.enqueuePendingCodexChildEvent(envelope.ThreadID, codexPendingChildEvent{
					kind: codexPendingRawExecution, raw: append(json.RawMessage(nil), raw...), params: append(json.RawMessage(nil), params...),
				})
			}
			return
		}
		a.replayPendingCodexChildEvents(envelope.ThreadID, route)
		sink = route.childSink
	}
	if err := agent.FlushPendingOutput(sink); err != nil {
		slog.Error("retry retained Codex execution output", "agent_id", a.AgentID(), "error", err)
	}
	a.Mu.Lock()
	turnID := a.mainTurn.id
	if !mainThread {
		if state := a.collabChildren[envelope.ThreadID]; state != nil {
			turnID = state.turnID
		} else {
			turnID = ""
		}
	}
	key := codexRawExecutionKey{threadID: envelope.ThreadID, turnID: envelope.TurnID, callID: item.CallID}
	a.Mu.Unlock()
	if turnID == "" || turnID != envelope.TurnID {
		return
	}
	switch item.Type {
	case contracts.CodexRawItemCustomToolCall:
		a.startCodexRawExecution(key, raw, sink)
	case contracts.CodexRawItemCustomToolCallOutput:
		a.handleCodexExecOutput(key, raw, item.Output)
	case codexRawItemFunctionCall:
		a.startCodexCellWait(key, envelope.Item)
	case codexRawItemFunctionCallOutput:
		a.handleCodexCellWaitOutput(key, item.Output)
	}
}

// codexRawExecutionItemAccepted reports whether one raw item can belong to a
// code-mode cell: an exec call, an exec output, a wait call, or a function
// output that can answer a wait. The call ID decides the last one later.
func codexRawExecutionItemAccepted(item contracts.CodexRawExecutionItem) bool {
	if item.Namespace != "" && item.Namespace != contracts.CodexRawNamespaceFunctions {
		return false
	}
	switch item.Type {
	case contracts.CodexRawItemCustomToolCall:
		return item.Name == contracts.CodexRawToolExec && item.Input != nil
	case contracts.CodexRawItemCustomToolCallOutput:
		return item.Name == "" || item.Name == contracts.CodexRawToolExec
	case codexRawItemFunctionCall:
		return item.Name == codexRawToolWait
	case codexRawItemFunctionCallOutput:
		return item.Name == "" || item.Name == codexRawToolWait
	default:
		return false
	}
}

// startCodexRawExecution records one exec request. Its row waits for the end
// of the cell, because a nested typed tool row can still cover the cell.
func (a *Agent) startCodexRawExecution(key codexRawExecutionKey, raw json.RawMessage, sink agent.ProviderServices) {
	if a.rawExecutionCalls[key] != nil {
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
		a.finishCodexRawExecution(oldest, agent.MessageCompletionInterrupted)
	}
	// Capture the owner now. Capture row geometry only when the pair reaches storage.
	captured := sink.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	call := &codexRawExecutionCall{
		request: agent.MessageContent{Original: append([]byte(nil), raw...), AgentSessionID: captured.AgentSessionID},
		sink:    sink, order: a.rawExecutionOrder,
	}
	if captured.Publication != nil {
		call.owner = captured.Publication.Owner()
	}
	a.rawExecutionCalls[key] = call
	a.rawExecutionOrder++
}

// handleCodexExecOutput reads the exec output. A yield keeps the cell open
// for its wait calls. A final status ends the cell.
func (a *Agent) handleCodexExecOutput(key codexRawExecutionKey, raw, output json.RawMessage) {
	call := a.rawExecutionCalls[key]
	if call == nil || call.completed || !codexRawOutputWellFormed(output) {
		return
	}
	if call.output.Original == nil {
		call.output = agent.MessageContent{Original: append([]byte(nil), raw...), AgentSessionID: call.request.AgentSessionID}
		segment := parseCodexExecSegment(output, call.commands)
		if segment.running {
			call.cellID = segment.cellID
			return
		}
		if a.endCodexCell(call, output, segment) {
			return
		}
	}
	// A repeated exec output of a cell that still runs changes nothing. A
	// repeated output of an ended cell retries a failed write.
	if call.ended {
		a.persistCodexRawExecution(key, call)
	}
}

// startCodexCellWait records a wait call on a cell that still runs. A wait on
// a cell that LeapMux does not track writes nothing.
func (a *Agent) startCodexCellWait(key codexRawExecutionKey, rawItem json.RawMessage) {
	var item struct {
		Arguments string `json:"arguments"`
	}
	var arguments struct {
		CellID string `json:"cell_id"`
	}
	if json.Unmarshal(rawItem, &item) != nil || json.Unmarshal([]byte(item.Arguments), &arguments) != nil || arguments.CellID == "" {
		return
	}
	for cellKey, call := range a.rawExecutionCalls {
		if cellKey.threadID != key.threadID || cellKey.turnID != key.turnID || call.cellID != arguments.CellID ||
			call.completed || call.ended {
			continue
		}
		if call.waits == nil {
			call.waits = make(map[string]struct{})
		}
		call.waits[key.callID] = struct{}{}
		return
	}
}

// handleCodexCellWaitOutput reads one wait output. The output joins the cell
// that the wait targets. A yield keeps the cell open, and a final status ends it.
func (a *Agent) handleCodexCellWaitOutput(waitKey codexRawExecutionKey, output json.RawMessage) {
	if !codexRawOutputWellFormed(output) {
		return
	}
	for key, call := range a.rawExecutionCalls {
		if key.threadID != waitKey.threadID || key.turnID != waitKey.turnID {
			continue
		}
		if _, waiting := call.waits[waitKey.callID]; !waiting {
			continue
		}
		delete(call.waits, waitKey.callID)
		if call.completed || call.ended {
			return
		}
		call.continuations = append(call.continuations, append(json.RawMessage(nil), bytes.TrimSpace(output)...))
		segment := parseCodexExecSegment(output, call.commands)
		if segment.running {
			return
		}
		if !a.endCodexCell(call, nil, segment) {
			a.persistCodexRawExecution(key, call)
		}
		return
	}
}

// endCodexCell records that the cell ended and decides its card once. It
// reports true when the rule suppresses the card. execOutput is the exec output
// when the caller holds it decoded, or nil to read it from the stored frame.
func (a *Agent) endCodexCell(call *codexRawExecutionCall, execOutput json.RawMessage, last codexExecSegment) bool {
	call.ended = true
	if execOutput == nil {
		execOutput = codexStoredExecOutput(call.output.Original)
	}
	outputs := call.outputs(execOutput)
	if last.completed && max(last.content, codexCellContent(outputs[:len(outputs)-1], call.commands)) == codexExecContentPolls {
		call.release()
		return true
	}
	return false
}

// codexStoredExecOutput reads the `output` value of a stored exec output frame.
func codexStoredExecOutput(frame []byte) json.RawMessage {
	var decoded struct {
		Params contracts.CodexRawExecutionParams `json:"params"`
	}
	var item contracts.CodexRawExecutionItem
	if json.Unmarshal(frame, &decoded) != nil || json.Unmarshal(decoded.Params.Item, &item) != nil {
		return nil
	}
	return item.Output
}

// pendingCodexChildWait reports whether the queue of an unrouted child holds
// the wait call with this call ID.
func (a *Agent) pendingCodexChildWait(threadID, callID string) bool {
	a.Mu.Lock()
	var events []codexPendingChildEvent
	if state := a.collabChildren[threadID]; state != nil {
		events = state.pendingEvents
	}
	a.Mu.Unlock()
	for _, event := range events {
		if event.kind != codexPendingRawExecution {
			continue
		}
		var envelope contracts.CodexRawExecutionParams
		var item contracts.CodexRawExecutionItem
		if json.Unmarshal(event.params, &envelope) == nil && json.Unmarshal(envelope.Item, &item) == nil &&
			item.Type == codexRawItemFunctionCall && item.CallID == callID {
			return true
		}
	}
	return false
}

// codexJSONRPCParams is the key of the JSON-RPC params in a stored raw frame.
const codexJSONRPCParams = "params"

// resolveCodexExecSupplement puts the wait outputs of a code cell on the exec
// output item that they continue, under the LeapMux key continuationOutputs.
// It reports false when the supplement does not join this row.
//
// The stored row is the whole rawResponseItem/completed frame, so the item is
// at params.item. A frame without an object at params holds the item at its
// own top level. The browser reads the same two places
// (frontend/src/components/chat/providers/codex/resolveMessage.ts).
func resolveCodexExecSupplement(content agent.MessageContent) ([]byte, bool) {
	var extra contracts.CodexExecSupplement
	if json.Unmarshal(content.Supplemental, &extra) != nil || extra.CallID == "" || len(extra.ContinuationOutputs) == 0 {
		return nil, false
	}
	var frame map[string]json.RawMessage
	if json.Unmarshal(content.Original, &frame) != nil || frame == nil {
		return nil, false
	}
	params := frame
	var nested map[string]json.RawMessage
	if json.Unmarshal(frame[codexJSONRPCParams], &nested) == nil && nested != nil {
		params = nested
	}
	var item map[string]json.RawMessage
	if json.Unmarshal(params[contracts.CodexRawFieldItem], &item) != nil || item == nil {
		return nil, false
	}
	var itemType, callID string
	if json.Unmarshal(item[contracts.CodexRawFieldType], &itemType) != nil || itemType != contracts.CodexRawItemCustomToolCallOutput {
		return nil, false
	}
	if json.Unmarshal(item[contracts.CodexRawFieldCallID], &callID) != nil || callID != extra.CallID {
		return nil, false
	}
	encodedOutputs, err := json.Marshal(extra.ContinuationOutputs)
	if err != nil {
		return nil, false
	}
	item[contracts.CodexSupplementContinuationOutputs] = encodedOutputs
	encodedItem, err := json.Marshal(item)
	if err != nil {
		return nil, false
	}
	params[contracts.CodexRawFieldItem] = encodedItem
	if nested != nil {
		encodedParams, err := json.Marshal(params)
		if err != nil {
			return nil, false
		}
		frame[codexJSONRPCParams] = encodedParams
	}
	resolved, err := json.Marshal(frame)
	if err != nil {
		return nil, false
	}
	return resolved, true
}

// codexRawOutputWellFormed reports whether a raw `output` value is the text
// or the content list that Codex writes.
func codexRawOutputWellFormed(output json.RawMessage) bool {
	output = bytes.TrimSpace(output)
	return len(output) > 0 && (output[0] == '"' || output[0] == '[')
}

const codexExecTimingOutputPattern = `\nWall time \d+(?:\.\d+)? seconds(?: \(code-mode [\d.]+ seconds; overhead -?[\d.]+ seconds\))?\nOutput:\n`

var codexCompletedExecHeader = regexp.MustCompile(`^` + regexp.QuoteMeta(contracts.CodexExecOutputCompletedHeader) + codexExecTimingOutputPattern + `$`)

// codexRunningExecHeader matches the header of a yield, and its first group is
// the cell ID. The text after the header is output of the cell.
var codexRunningExecHeader = regexp.MustCompile(`^` + regexp.QuoteMeta(contracts.CodexExecOutputRunningHeaderPrefix) + `(\S+)` + codexExecTimingOutputPattern)

// codexExecContent ranks what one output of a cell holds. A cell holds the
// largest rank of its outputs.
type codexExecContent int

const (
	// codexExecContentNone: no output at all.
	codexExecContentNone codexExecContent = iota
	// codexExecContentPolls: empty polls or receipts that stored commands already show.
	codexExecContentPolls
	// codexExecContentOther: output that no typed row shows.
	codexExecContentOther
)

// codexExecSegment is one output of a cell: the exec output or a wait output.
type codexExecSegment struct {
	// running reports a yield. cellID then identifies the cell.
	running bool
	cellID  string
	// completed reports the header "Script completed" alone in its block.
	completed bool
	content   codexExecContent
}

// codexExecBlock retains ambiguity that a plain struct decoder would discard.
type codexExecBlock struct {
	Type      string
	Text      string
	ambiguous bool
}

func (block *codexExecBlock) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("the Codex content block is not an object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("the Codex content key is not a string")
		}
		block.ambiguous = block.ambiguous || seen[key]
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		switch key {
		case "type", "text":
			var text string
			if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &text) != nil {
				block.ambiguous = true
				continue
			}
			if key == "type" {
				block.Type = text
			} else {
				block.Text = text
			}
		default:
			block.ambiguous = true
		}
	}
	_, err = decoder.Token()
	block.ambiguous = block.ambiguous || !seen["type"] || !seen["text"]
	return err
}

// parseCodexExecSegment reads the status and the content of one output.
//
// Text output keeps the rule that it had before cells: completed text output
// always shows, and so does a yield with text after its header.
func parseCodexExecSegment(raw json.RawMessage, commands map[codexStoredCommandResult]struct{}) codexExecSegment {
	other := codexExecSegment{content: codexExecContentOther}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return other
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return other
		}
		if segment, ok := parseCodexRunningSegment(text); ok {
			return segment
		}
		return other
	}
	var blocks []codexExecBlock
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 || blocks[0].Type != contracts.CodexExecOutputTextContent {
		return other
	}
	var segment codexExecSegment
	if running, ok := parseCodexRunningSegment(blocks[0].Text); ok {
		segment = running
		if segment.content == codexExecContentOther {
			return segment
		}
	} else if codexCompletedExecHeader.MatchString(blocks[0].Text) {
		segment = codexExecSegment{completed: true, content: codexExecContentNone}
	} else {
		return other
	}
	if blocks[0].ambiguous {
		segment.content = codexExecContentOther
		return segment
	}
	for _, block := range blocks[1:] {
		switch {
		case block.ambiguous || block.Type != contracts.CodexExecOutputTextContent:
			segment.content = codexExecContentOther
		case block.Text == "" && segment.running:
			// An empty block of a yield holds nothing.
		case codexCoveredReceiptJSON(block.Text, commands):
			segment.content = max(segment.content, codexExecContentPolls)
		default:
			segment.content = codexExecContentOther
		}
		if segment.content == codexExecContentOther {
			return segment
		}
	}
	return segment
}

// parseCodexRunningSegment preserves every byte after a recognized yield header.
func parseCodexRunningSegment(text string) (codexExecSegment, bool) {
	match := codexRunningExecHeader.FindStringSubmatch(text)
	if match == nil {
		return codexExecSegment{}, false
	}
	segment := codexExecSegment{running: true, cellID: match[1]}
	if len(text) > len(match[0]) {
		segment.content = codexExecContentOther
	}
	return segment, true
}

// codexSuppressCellOutputs accepts only completed cells of empty poll receipts.
func codexSuppressCellOutputs(outputs ...json.RawMessage) bool {
	if len(outputs) == 0 {
		return false
	}
	last := parseCodexExecSegment(outputs[len(outputs)-1], nil)
	return last.completed && max(last.content, codexCellContent(outputs[:len(outputs)-1], nil)) == codexExecContentPolls
}

// codexCellContent stops when one segment requires a visible card.
func codexCellContent(outputs []json.RawMessage, commands map[codexStoredCommandResult]struct{}) codexExecContent {
	content := codexExecContentNone
	for _, output := range outputs {
		content = max(content, parseCodexExecSegment(output, commands).content)
		if content == codexExecContentOther {
			break
		}
	}
	return content
}

// Match encoding/json's nesting limit. Token traversal otherwise has no nesting limit.
const codexPollJSONDepthLimit = 10000

// Read exactly one printed JSON value. Stop when content makes suppression unsafe.
// Tokens preserve duplicate keys and avoid allocating a complete tree for rejected output.
func codexCoveredReceiptJSON(text string, commands map[codexStoredCommandResult]struct{}) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || !codexCoveredReceiptValue(decoder, token, 0, commands) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

// codexCoveredReceiptValue accepts empty polls or receipts that stored commands already show.
// A script can print receipts in arrays or under container keys, including receipt field keys.
// These values keep the execution pair:
//   - A string outside a recognized receipt or status field.
//   - A boolean or null.
//   - A number outside a recognized receipt field or an i/index label.
//   - A rejected record.
//   - A duplicate object key.
func codexCoveredReceiptValue(decoder *json.Decoder, token json.Token, depth int, commands map[codexStoredCommandResult]struct{}) bool {
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
			if err != nil || !codexCoveredReceiptValue(decoder, item, depth+1, commands) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	case json.Delim('{'):
		return codexCoveredReceiptObject(decoder, depth, commands)
	default:
		return false
	}
}

// Classify each object as a receipt or a container while reading each field once.
// Nested receipts can use any key except status, which identifies a settled record.
// A scalar receipt field cannot become a container label when receipt validation fails.
func codexCoveredReceiptObject(decoder *json.Decoder, depth int, commands map[codexStoredCommandResult]struct{}) bool {
	fields := make(map[string]bool)
	receipt, emptyReceipt, container := true, true, true
	var result codexStoredCommandResult
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
			if !codexCoveredReceiptValue(decoder, value, depth+1, commands) {
				return false
			}
			receipt = false
			container = container && key != "status"
			foundPoll = true
			if key == "value" {
				valuePoll = true
			}
		} else {
			emptyReceipt = emptyReceipt && codexEmptyPollReceiptField(key, value)
			valid := codexEmptyPollReceiptField(key, value)
			if len(commands) > 0 {
				switch key {
				case "output":
					result.output, valid = value.(string)
				case "exit_code":
					result.exitCode, valid = codexJSONInteger(value)
				case "original_token_count":
					count, ok := codexJSONInteger(value)
					valid = ok && count >= 0
				}
			}
			receipt = receipt && valid
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
		complete := fields["chunk_id"] && fields["wall_time_seconds"] && fields["original_token_count"] && fields["output"]
		if !complete {
			return false
		}
		if emptyReceipt {
			return fields["session_id"] || fields["exit_code"]
		}
		if !fields["exit_code"] || fields["session_id"] {
			return false
		}
		_, stored := commands[result]
		return stored
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
	call.waits = nil
	call.continuations = nil
	call.commands = nil
}

func (a *Agent) persistCodexRawExecution(key codexRawExecutionKey, call *codexRawExecutionCall) {
	if (call.owner != nil && !call.owner.IsCurrent()) || a.IsDiscardingOutput() {
		call.release()
		return
	}
	if len(call.continuations) > 0 && call.output.Supplemental == nil {
		supplement, err := json.Marshal(contracts.CodexExecSupplement{CallID: key.callID, ContinuationOutputs: call.continuations})
		if err != nil {
			slog.Error("Cannot encode the Codex cell output", "agent_id", a.AgentID(), "call_id", key.callID, "error", err)
			call.release()
			return
		}
		call.output.Supplemental = supplement
	}
	sink := call.sink
	if agent.OutputQueueFor(sink) == nil {
		sink = agent.NewOrderedProviderServices(a.generationBuffer.OutputQueue(), sink)
	}
	defer sink.CloseSpan(key.callID)
	call.color = sink.ReserveSpanColor(key.callID, "")
	opening := agent.SpanInfo{SpanID: key.callID, SpanType: contracts.CodexRawToolExec, SpanColor: call.color}
	request := sink.CaptureMessage(call.request, opening)
	sink.SetSpanType(key.callID, contracts.CodexRawToolExec)
	sink.OpenSpan(key.callID, "")
	closing := opening
	closing.Closing = true
	output := sink.CaptureMessage(call.output, closing)
	err := agent.PersistTranscriptBatch(sink, []agent.TranscriptMessage{
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: request, Span: opening},
		{Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: output, Span: closing},
	})
	if err != nil {
		slog.Error("persist the Codex execution pair", "agent_id", a.AgentID(), "call_id", key.callID, "error", err)
		if !agent.OutputRetained(err) {
			return
		}
	}
	call.release()
	sink.ReportProgress(agent.CompleteOutputProgress(key.callID))
	if a.isMainThreadID(key.threadID) {
		a.Mu.Lock()
		a.TurnToolUses++
		a.Mu.Unlock()
	}
}

// codexStoredCommandResult proves which output and exit status already reached storage.
type codexStoredCommandResult struct {
	output   string
	exitCode int64
}

// observeCodexRawExecutionTool records only a successfully stored command completion.
// Temporal overlap alone cannot prove that a typed row covers a cell's output.
func (a *Agent) observeCodexRawExecutionTool(event codexItemEvent) {
	if event.itemType != contracts.CodexItemTypeCommandExecution || event.itemID == "" || event.threadID == "" || event.turnID == "" {
		return
	}
	var command struct {
		Output   *string `json:"aggregatedOutput"`
		ExitCode *int64  `json:"exitCode"`
	}
	if json.Unmarshal(event.item, &command) != nil || command.Output == nil || command.ExitCode == nil {
		return
	}
	result := codexStoredCommandResult{output: *command.Output, exitCode: *command.ExitCode}
	for key, call := range a.rawExecutionCalls {
		if key.threadID == event.threadID && key.turnID == event.turnID && call.awaited() {
			if call.commands == nil {
				call.commands = make(map[codexStoredCommandResult]struct{})
			}
			call.commands[result] = struct{}{}
		}
	}
}

// Retry eligible pairs once before the turn ends. Requests without output stay absent from history.
//
// A cell that still runs keeps the output that it stated, with the retained
// completion of the turn, because LeapMux reads no more of the cell after the
// turn. A cell that holds no output, or only empty shell-poll receipts, keeps
// no card. Other output remains visible even when a typed tool ran nearby.
func (a *Agent) finishCodexRawExecutions(threadID string, completion agent.MessageCompletion) {
	a.finishMatchingCodexRawExecutions(completion, func(key codexRawExecutionKey) bool { return key.threadID == threadID })
	if sink, ready := a.codexReadyThreadSink(threadID); ready {
		if err := agent.FlushPendingOutput(sink); err != nil {
			slog.Error("retry Codex output at turn end", "error", err)
		}
	}
}

// finishAllCodexRawExecutions retains output before the process or thread tree ends.
// The caller holds outputMu and keeps each old transcript owner available.
func (a *Agent) finishAllCodexRawExecutions(completion agent.MessageCompletion) {
	a.finishMatchingCodexRawExecutions(completion, func(codexRawExecutionKey) bool { return true })
}

func (a *Agent) finishMatchingCodexRawExecutions(completion agent.MessageCompletion, matches func(codexRawExecutionKey) bool) {
	var keys []codexRawExecutionKey
	for key := range a.rawExecutionCalls {
		if matches(key) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return a.rawExecutionCalls[keys[i]].order < a.rawExecutionCalls[keys[j]].order })
	for _, key := range keys {
		a.finishCodexRawExecution(key, completion)
	}
}

// finishCodexRawExecution retains available output before it removes a cell.
// Every lifecycle removal uses this rule, including capacity eviction.
func (a *Agent) finishCodexRawExecution(key codexRawExecutionKey, completion agent.MessageCompletion) {
	call := a.rawExecutionCalls[key]
	if call == nil {
		return
	}
	switch {
	case call.completed || call.output.Original == nil:
	case call.ended:
		a.persistCodexRawExecution(key, call)
	case codexCellContent(call.outputs(codexStoredExecOutput(call.output.Original)), nil) == codexExecContentOther:
		call.output.Completion = codexRetainedCompletion(completion)
		a.persistCodexRawExecution(key, call)
	}
	delete(a.rawExecutionCalls, key)
}

// codexRetainedCompletion is the completion of a call that the turn ended
// before the call did. A turn that completed still cut the call.
func codexRetainedCompletion(completion agent.MessageCompletion) agent.MessageCompletion {
	if completion == agent.MessageCompletionComplete {
		return agent.MessageCompletionInterrupted
	}
	return completion
}

// A new turn retains prior output even when the preceding turn sent no completion.
func (a *Agent) clearCodexRawExecutionsBeforeTurn(threadID, turnID string) {
	a.finishMatchingCodexRawExecutions(agent.MessageCompletionInterrupted, func(key codexRawExecutionKey) bool {
		return key.threadID == threadID && key.turnID != turnID
	})
}
