package amp

import (
	"encoding/json"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ampLine takes the fields of a stdout line that the worker reads. Amp prints
// five line shapes: the init `system` line, `user` lines (the echo of a user
// message, or tool results), `assistant` lines, and a success or error
// `result`.
type ampLine struct {
	Type      string      `json:"type"`
	Subtype   string      `json:"subtype"`
	SessionID string      `json:"session_id"`
	Message   *ampMessage `json:"message"`
	IsError   bool        `json:"is_error"`
	Error     string      `json:"error"`
}

// ampMessage is the message of an assistant or user line. Amp prints each
// assistant message WHOLE, once, when it completes.
type ampMessage struct {
	Content    []json.RawMessage `json:"content"`
	StopReason *string           `json:"stop_reason"`
	Usage      *ampUsage         `json:"usage"`
}

// ampUsage is the token usage of one assistant message. Amp states no model, no
// context window and no cost on the stream.
type ampUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

// ampBlock takes the fields of one content block that the worker reads.
type ampBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// toolResultText is the text of a tool result. Amp states it as a string. A
// list of text blocks, which an older Amp build states, gives its texts joined.
func toolResultText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []ampBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == contracts.AmpBlockTypeText {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// resultLine is the `result` line that ends a turn. Amp prints one for each
// PROCESS, when stdin closes or an error or a signal ends the process. For a
// turn that ends inside a process -- every turn but the last -- the worker
// writes the same shape itself, with the turn's own count and text, which is
// what Amp prints for a process that ran that one turn. The turn's duration is
// the worker's own measurement and travels as metadata.
type resultLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	NumTurns  int    `json:"num_turns"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	SessionID string `json:"session_id"`
}

// handleLine dispatches one stdout line of proc. It runs on the process's
// reader goroutine, one line at a time.
func (a *Agent) handleLine(proc *ampProcess, line *providerkit.ParsedLine) {
	if a.discarding() || line == nil {
		return
	}
	switch line.Type {
	case contracts.AmpLineTypeSystem:
		a.handleSystemLine(proc, line.Raw)
	case contracts.AmpLineTypeAssistant:
		a.handleAssistantLine(line.Raw)
	case contracts.AmpLineTypeUser:
		a.handleUserLine(line.Raw)
	case contracts.AmpLineTypeResult:
		a.handleResultLine(proc, line.Raw)
	default:
		// A line this build does not know reaches the transcript as an
		// inspectable card rather than disappearing.
		a.persistRaw(line.Raw)
	}
}

// handleSystemLine reads the init line, the one `system` line Amp prints. It
// states the thread id, which is the resume handle.
//
// The line's `agent_mode` is NOT read: on `threads continue` Amp reports its own
// `--mode` default there, not the mode the thread keeps.
func (a *Agent) handleSystemLine(proc *ampProcess, raw []byte) {
	var line ampLine
	if err := json.Unmarshal(raw, &line); err != nil {
		slog.Warn("amp system line decode failed", "agent_id", a.agentID, "error", err)
		return
	}
	if line.Subtype != systemSubtypeInit {
		a.persistRaw(raw)
		return
	}
	if proc != nil {
		proc.sawInit.Store(true)
	}
	if line.SessionID == "" {
		return
	}
	a.mu.Lock()
	previous := a.threadID
	a.threadID = line.SessionID
	a.mu.Unlock()
	if previous == line.SessionID {
		return
	}
	if previous != "" {
		slog.Warn("amp reported a different thread", "agent_id", a.agentID, "expected", previous, "thread_id", line.SessionID)
	}
	a.sink.UpdateSessionID(line.SessionID)
	a.sink.BroadcastStatusActive(line.SessionID)
}

// handleAssistantLine persists one assistant message and ends the turn when the
// message ends it.
//
// Amp prints a message WHOLE, with every block in one line. The transcript takes
// one row for each block: a tool call owns a span of its own, and a row can open
// only one. Each row is Amp's own line with its content cut to that one block
// and every other field kept. The context usage that the worker calculates
// rides on the message's last row alone.
//
// Nothing here feeds the live generation counter. A message arrives only when
// it is complete, at the same moment as the row that shows it, and each row
// resets that counter, so no count could stay visible.
func (a *Agent) handleAssistantLine(raw []byte) {
	var line ampLine
	if err := json.Unmarshal(raw, &line); err != nil || line.Message == nil {
		slog.Warn("amp assistant line decode failed", "agent_id", a.agentID, "error", err)
		a.persistRaw(raw)
		return
	}
	a.ensureTurn()

	rows, err := splitBlocks(raw)
	if err != nil {
		slog.Warn("amp assistant line split failed", "agent_id", a.agentID, "error", err)
		a.persistRaw(raw)
		return
	}
	usageMetadata := a.recordUsage(line.Message.Usage)
	lastKept := -1
	for i, row := range rows {
		if assistantBlockKept(row.block) {
			lastKept = i
		}
	}
	for i, row := range rows {
		if !assistantBlockKept(row.block) {
			continue
		}
		content := agent.MessageContent{Original: row.line}
		if i == lastKept {
			content.Metadata = usageMetadata
		}
		switch row.block.Type {
		case contracts.AmpBlockTypeToolUse:
			a.openToolCall(row.block, content)
		default:
			if row.block.Type == contracts.AmpBlockTypeText {
				a.noteAssistantText(row.block.Text)
			}
			if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
				slog.Error("amp persist assistant row", "agent_id", a.agentID, "error", err)
			}
		}
	}

	a.mu.Lock()
	if a.turn.active {
		a.turn.assistantMessages++
	}
	a.mu.Unlock()
	if line.Message.StopReason != nil && *line.Message.StopReason == stopReasonEndTurn {
		a.finishTurn(agent.MessageCompletionComplete, nil)
	}
}

// assistantBlockKept reports whether one assistant block reaches the transcript.
// An empty text or thinking block states nothing: an OpenAI model sends an empty
// thinking block for its encrypted reasoning. A redacted thinking block holds
// only data the reader cannot read.
func assistantBlockKept(block ampBlock) bool {
	switch block.Type {
	case contracts.AmpBlockTypeText:
		return strings.TrimSpace(block.Text) != ""
	case contracts.AmpBlockTypeThinking:
		return strings.TrimSpace(block.Thinking) != ""
	case contracts.AmpBlockTypeRedactedThinking:
		return false
	default:
		// A tool call, or a block this build does not know, which reaches the
		// transcript as an inspectable card.
		return true
	}
}

// ensureTurn arms a turn for an assistant message that arrived with none armed.
// Amp starts one by itself when a steering message reached its queue after the
// turn it was meant for ended: the server then runs the message as a turn of
// its own.
func (a *Agent) ensureTurn() {
	now := a.now()
	a.mu.Lock()
	if a.turn.active {
		a.mu.Unlock()
		return
	}
	a.turn = turnState{active: true, startedAt: now}
	a.mu.Unlock()
	a.PublishTurnActive()
}

// noteAssistantText records the text of the turn's latest assistant message,
// which the turn's `result` states.
func (a *Agent) noteAssistantText(text string) {
	a.mu.Lock()
	if a.turn.active {
		a.turn.lastText = text
	}
	a.mu.Unlock()
}

// handleUserLine persists the tool results of one user line.
//
// A user line with text is Amp's echo of a message LeapMux sent -- the turn's
// prompt, or a steering message at the moment Amp inserts it -- and LeapMux
// already persisted that message, so the worker drops the echo.
func (a *Agent) handleUserLine(raw []byte) {
	rows, err := splitBlocks(raw)
	if err != nil {
		slog.Warn("amp user line split failed", "agent_id", a.agentID, "error", err)
		return
	}
	for _, row := range rows {
		if row.block.Type == contracts.AmpBlockTypeToolResult {
			a.closeToolCall(row.block, row.line)
		}
	}
}

// handleResultLine ends the process's work. Amp prints `result` once, just
// before it exits: after stdin closed and the agent went idle, after an error
// ended the session, or after a signal. So the result ends whatever turn ran,
// and the next message starts a new process.
//
// An error that ended a turn the user did not stop asks the exit handler to
// resume the thread at once. The transcript states an error with no turn
// running, and a success with no turn running (the answer to closing stdin)
// states nothing.
func (a *Agent) handleResultLine(proc *ampProcess, raw []byte) {
	if proc != nil {
		proc.sawResult.Store(true)
		proc.markEnding()
	}
	var line ampLine
	if err := json.Unmarshal(raw, &line); err != nil {
		slog.Warn("amp result line decode failed", "agent_id", a.agentID, "error", err)
	}
	ended := false
	a.endTurn(func(turn turnState) (agent.MessageCompletion, []byte) {
		ended = true
		switch {
		case turn.interruptRequested:
			return agent.MessageCompletionInterrupted, raw
		case line.IsError:
			if proc != nil {
				proc.resumeAfterExit.Store(true)
			}
			return agent.MessageCompletionError, raw
		default:
			return agent.MessageCompletionComplete, raw
		}
	})
	if !ended && line.IsError {
		a.sink.PersistLeapMuxNotification(map[string]any{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: line.Error,
		})
	}
}

// openToolCall records one tool call and persists its opening row.
func (a *Agent) openToolCall(block ampBlock, content agent.MessageContent) {
	if block.ID == "" {
		slog.Warn("amp tool call with no id", "agent_id", a.agentID, "tool", block.Name)
		a.persistRaw(content.Original)
		return
	}
	tool := &openTool{
		id:       block.ID,
		name:     block.Name,
		input:    append(json.RawMessage(nil), block.Input...),
		row:      content.Original,
		subagent: isSubagentTool(block.Name),
	}
	a.mu.Lock()
	tool.order = a.nextOrder
	a.nextOrder++
	a.tools[block.ID] = tool
	a.mu.Unlock()
	// A permission request can arrive before the agent reads this line. The
	// request waits for the call to appear.
	a.bridge.noteToolCall()

	if tool.subagent {
		a.openSubagentRow(tool)
	}
	if err := providerkit.OpenToolSpan(a.sink, content, block.ID, block.Name, false); err != nil {
		slog.Error("amp persist tool call", "agent_id", a.agentID, "tool", block.Name, "error", err)
	}
}

// closeToolCall persists one tool result and closes its span.
func (a *Agent) closeToolCall(block ampBlock, row []byte) {
	if block.ToolUseID == "" {
		a.persistRaw(row)
		return
	}
	a.mu.Lock()
	tool := a.tools[block.ToolUseID]
	delete(a.tools, block.ToolUseID)
	if a.turn.active {
		a.turn.toolUses++
	}
	a.mu.Unlock()

	name := ""
	if tool != nil {
		name = tool.name
	}
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: row}, agent.SpanInfo{
		SpanID:   block.ToolUseID,
		SpanType: name,
		Closing:  true,
	}); err != nil {
		slog.Error("amp persist tool result", "agent_id", a.agentID, "tool", name, "error", err)
	}
	a.sink.CloseSpan(block.ToolUseID)
	if tool == nil {
		return
	}
	if tool.subagent {
		a.closeSubagentRow(tool.id, subagentStatus(block.IsError))
	}
	a.noteShellResult(tool, toolResultText(block.Content))
}

// finishTurn ends the running turn with a completion the caller already knows.
// resultLine is the `result` Amp printed, or nil for a turn that ended inside
// the process, whose row the worker writes itself.
func (a *Agent) finishTurn(completion agent.MessageCompletion, resultLine []byte) {
	a.endTurn(func(turnState) (agent.MessageCompletion, []byte) { return completion, resultLine })
}

// endTurn ends the running turn: it closes the tool calls the turn left open,
// persists the turn-end row, and clears the flag. decide states the turn's
// completion and its `result` line from the turn itself, read under the same
// lock that ends it, so an interrupt that lands a moment earlier is never
// lost. A nil line makes the worker write one: Amp's success shape for a
// complete turn, and its error shape otherwise.
//
// The worker persists the row BEFORE the flag clears, because the clear is the
// settle edge that spends the turn's tool count (see
// agent.TranscriptServices.PersistTurnEnd).
func (a *Agent) endTurn(decide func(turn turnState) (agent.MessageCompletion, []byte)) {
	endedAt := a.now()
	a.mu.Lock()
	if !a.turn.active {
		a.mu.Unlock()
		return
	}
	turn := a.turn
	completion, resultLine := decide(turn)
	a.turn = turnState{}
	open := a.takeOpenToolsLocked()
	threadID := a.threadID
	usage := maps.Clone(a.contextUsage)
	a.mu.Unlock()

	// A permission request outlives no turn: every tool of the turn ended.
	a.bridge.cancelAll(errTurnEnded)
	a.closeIncompleteTools(open, completion)
	if resultLine == nil {
		if completion == agent.MessageCompletionComplete {
			resultLine = successResultLine(threadID, turn)
		} else {
			resultLine = errorResultLine(threadID, interruptedMessage)
		}
	}
	if !a.discarding() {
		content := agent.MessageContent{
			Original:   resultLine,
			Completion: completion,
			Metadata:   turnMetadata(usage, turnDurationMs(turn, endedAt)),
		}
		if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(content, turn.toolUses), agent.SpanInfo{}); err != nil {
			slog.Error("amp persist turn end", "agent_id", a.agentID, "error", err)
		}
	}
	a.sink.ResetSpans()
	a.PublishTurnActive()
}

// takeOpenToolsLocked removes every open tool call, in the order they started.
// The caller holds mu.
func (a *Agent) takeOpenToolsLocked() []*openTool {
	open := make([]*openTool, 0, len(a.tools))
	for _, tool := range a.tools {
		open = append(open, tool)
	}
	clear(a.tools)
	sort.Slice(open, func(i, j int) bool { return open[i].order < open[j].order })
	return open
}

// closeIncompleteTools closes each tool call its turn outlived. The closing row
// is the call's own opening row again, with the completion stating that the
// call did not finish, so the transcript never holds a line Amp did not print.
func (a *Agent) closeIncompleteTools(open []*openTool, completion agent.MessageCompletion) {
	for _, tool := range open {
		if !a.discarding() {
			if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
				Original:   tool.row,
				Completion: completion,
			}, agent.SpanInfo{SpanID: tool.id, SpanType: tool.name, Closing: true}); err != nil {
				slog.Error("amp persist incomplete tool", "agent_id", a.agentID, "tool", tool.name, "error", err)
			}
		}
		a.sink.CloseSpan(tool.id)
		if tool.subagent {
			a.closeSubagentRow(tool.id, agent.IncompleteTaskStatus(completion))
		}
	}
}

// recordUsage broadcasts the context usage of one assistant message, and
// returns the metadata its row carries.
//
// Amp states no context window, so the reading holds the token counts and the
// context size alone. The context is every input token of the request:
// Amp's `input_tokens` counts only the uncached part, and is often zero.
func (a *Agent) recordUsage(usage *ampUsage) []byte {
	if usage == nil {
		return nil
	}
	reading := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
		Input:      usage.InputTokens,
		CacheWrite: usage.CacheCreationInputTokens,
		CacheRead:  usage.CacheReadInputTokens,
		Output:     usage.OutputTokens,
	})
	reading[contracts.ContextUsageFieldContextTokens] = usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	a.mu.Lock()
	changed := !maps.Equal(reading, a.contextUsage)
	a.contextUsage = reading
	a.mu.Unlock()
	if changed {
		a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: maps.Clone(reading)})
	}
	return turnMetadata(reading, nil)
}

// turnMetadata encodes the fields LeapMux calculates for a row: the context
// usage, and a turn's duration.
func turnMetadata(usage map[string]any, durationMs *int64) []byte {
	fields := map[string]any{}
	if len(usage) > 0 {
		fields[contracts.SessionInfoKeyContextUsage] = usage
	}
	if durationMs != nil {
		fields[contracts.MessageMetadataFieldDurationMs] = *durationMs
	}
	if len(fields) == 0 {
		return nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("amp encode row metadata", "error", err)
		return nil
	}
	return encoded
}

// turnDurationMs measures one turn, or nil for a clock that moved backwards.
func turnDurationMs(turn turnState, endedAt time.Time) *int64 {
	if turn.startedAt.IsZero() || endedAt.Before(turn.startedAt) {
		return nil
	}
	ms := endedAt.Sub(turn.startedAt).Milliseconds()
	return &ms
}

// successResultLine is the `result` of a turn that ended inside a process.
func successResultLine(threadID string, turn turnState) []byte {
	return encodeResultLine(resultLine{
		Type:      contracts.AmpLineTypeResult,
		Subtype:   contracts.AmpResultSubtypeSuccess,
		NumTurns:  turn.assistantMessages,
		Result:    turn.lastText,
		SessionID: threadID,
	})
}

// errorResultLine is the `result` of a turn whose process ended with none.
func errorResultLine(threadID, message string) []byte {
	return encodeResultLine(resultLine{
		Type:      contracts.AmpLineTypeResult,
		Subtype:   contracts.AmpResultSubtypeErrorDuringExecution,
		IsError:   true,
		Error:     message,
		SessionID: threadID,
	})
}

func encodeResultLine(line resultLine) []byte {
	encoded, err := json.Marshal(line)
	if err != nil {
		// Every field is a string, a bool or an int, which always encodes.
		panic(err)
	}
	return encoded
}

// persistRaw persists a line this build cannot read, so the reader can still
// inspect it.
func (a *Agent) persistRaw(raw []byte) {
	if err := a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}); err != nil {
		slog.Error("amp persist an unknown line", "agent_id", a.agentID, "error", err)
	}
}

// blockRow is one content block, and the line cut down to it.
type blockRow struct {
	block ampBlock
	line  []byte
}

// splitBlocks returns one row for each content block of an assistant or user
// line: Amp's own line, with `message.content` holding that block alone and
// every other field unchanged.
func splitBlocks(raw []byte) ([]blockRow, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(top["message"], &message); err != nil {
		return nil, err
	}
	var blocks []json.RawMessage
	if len(message["content"]) > 0 {
		if err := json.Unmarshal(message["content"], &blocks); err != nil {
			return nil, err
		}
	}
	rows := make([]blockRow, 0, len(blocks))
	for _, rawBlock := range blocks {
		var block ampBlock
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return nil, err
		}
		message["content"] = json.RawMessage("[" + string(rawBlock) + "]")
		encodedMessage, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		top["message"] = encodedMessage
		line, err := json.Marshal(top)
		if err != nil {
			return nil, err
		}
		rows = append(rows, blockRow{block: block, line: line})
	}
	return rows, nil
}
