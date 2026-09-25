package qwen

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
)

// A background subagent streams nothing over ACP. It writes its conversation
// to a transcript file of its own, one ChatRecord per line, as it runs:
//
//	<runtime>/projects/<cwd>/subagents/<parent session>/agent-<id>.jsonl
//
// Qwen's own `qwen serve` daemon shows a running background agent by reading
// that file as it grows, and so does this. It reads the file only, never
// writes it, and converts each record into the ACP update that Qwen streams
// for a foreground child, so the child's tab draws both kinds of subagent the
// same way.

// transcriptPollInterval is how often the reader looks for new records. Qwen's
// daemon reads at the same pace.
const transcriptPollInterval = 250 * time.Millisecond

// transcriptReadLimit caps one read of the file, so a transcript that grew by a
// large amount between two polls is read over several polls rather than into
// memory at once.
const transcriptReadLimit = 1 << 20

// transcriptLineLimit caps one record. A longer line is not a record that this
// reader can convert, and it is skipped whole.
const transcriptLineLimit = 16 << 20

// backgroundTranscriptPath checks the path that a spawn's result states. Only
// a JSONL file of an absolute path under a `subagents` directory is a subagent
// transcript: the reader must not follow a path that a model echoed anywhere
// else.
func backgroundTranscriptPath(stated string) (string, bool) {
	path := filepath.Clean(stated)
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".jsonl" {
		return "", false
	}
	if filepath.Base(filepath.Dir(filepath.Dir(path))) != "subagents" {
		return "", false
	}
	return path, true
}

// transcriptTail reads one background child's transcript as it grows.
type transcriptTail struct {
	agent  *Agent
	rowKey string
	path   string

	// mu serializes the reads: the poll loop and the final drain.
	mu      sync.Mutex
	offset  int64
	partial []byte
	// skipping holds that the reader drops the rest of a line that passed the
	// line limit.
	skipping bool
	convert  transcriptConverter

	started  atomic.Bool
	stopOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
}

func newTranscriptTail(a *Agent, rowKey, path string) *transcriptTail {
	return &transcriptTail{
		agent:   a,
		rowKey:  rowKey,
		path:    path,
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// start runs the poll loop. It runs once: a second call does nothing.
func (t *transcriptTail) start(clock quartz.Clock) {
	if !t.started.CompareAndSwap(false, true) {
		return
	}
	ticker := clock.NewTicker(transcriptPollInterval, "qwen", "transcript")
	go func() {
		defer close(t.stopped)
		defer ticker.Stop()
		for {
			select {
			case <-t.done:
				return
			case <-ticker.C:
				t.poll()
			}
		}
	}()
}

// stop ends the poll loop, and reads what the file holds when drain is set.
// It returns after the loop ended, so no read runs after it.
func (t *transcriptTail) stop(drain bool) {
	t.stopOnce.Do(func() { close(t.done) })
	if t.started.Load() {
		<-t.stopped
	}
	if drain {
		t.poll()
	}
}

// poll reads the complete records that the file gained since the last read,
// and feeds each one to the child's transcript.
func (t *transcriptTail) poll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		chunk, err := t.read()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("qwen background transcript read failed", "agent_id", t.agent.AgentID(), "row_key", t.rowKey, "error", err)
			}
			return
		}
		if len(chunk) == 0 {
			return
		}
		t.consume(chunk)
		if len(chunk) < transcriptReadLimit {
			return
		}
	}
}

// read returns the bytes after the offset, up to the read limit.
func (t *transcriptTail) read() ([]byte, error) {
	file, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	chunk, err := io.ReadAll(io.LimitReader(file, transcriptReadLimit))
	if err != nil {
		return nil, err
	}
	t.offset += int64(len(chunk))
	return chunk, nil
}

// consume splits a chunk into lines, keeps a trailing partial line for the
// next read, and feeds each complete record.
func (t *transcriptTail) consume(chunk []byte) {
	for len(chunk) > 0 {
		newline := bytes.IndexByte(chunk, '\n')
		if newline < 0 {
			if !t.skipping {
				t.partial = append(t.partial, chunk...)
				if len(t.partial) > transcriptLineLimit {
					t.partial = nil
					t.skipping = true
				}
			}
			return
		}
		line := chunk[:newline]
		chunk = chunk[newline+1:]
		if t.skipping {
			t.skipping = false
			continue
		}
		if len(t.partial) > 0 {
			line = append(t.partial, line...)
			t.partial = nil
		}
		t.feed(line)
	}
}

// feed converts one record and hands its updates to the child's transcript.
func (t *transcriptTail) feed(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var record chatRecord
	if err := json.Unmarshal(line, &record); err != nil {
		slog.Debug("qwen background transcript record unreadable", "agent_id", t.agent.AgentID(), "row_key", t.rowKey, "error", err)
		return
	}
	converted := t.convert.convert(record)
	for _, text := range converted.userTexts {
		t.agent.persistChildUserText(t.rowKey, text)
	}
	for _, update := range converted.updates {
		t.agent.FeedChildUpdate(t.rowKey, update)
	}
}

// chatRecord is the part of one Qwen ChatRecord that the reader converts.
type chatRecord struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	Message        *chatContent    `json:"message"`
	ToolCallResult *toolCallResult `json:"toolCallResult"`
}

// chatContent is a Gemini `Content`: a role and its parts.
type chatContent struct {
	Role  string     `json:"role"`
	Parts []chatPart `json:"parts"`
}

// chatPart is one part of a Gemini `Content`.
type chatPart struct {
	Text             *string           `json:"text"`
	Thought          bool              `json:"thought"`
	FunctionCall     *functionCall     `json:"functionCall"`
	FunctionResponse *functionResponse `json:"functionResponse"`
}

type functionCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type functionResponse struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Response map[string]json.RawMessage `json:"response"`
}

// toolCallResult is the part of a record's `toolCallResult` that the reader
// converts.
type toolCallResult struct {
	CallID        string          `json:"callId"`
	Status        string          `json:"status"`
	Error         json.RawMessage `json:"error"`
	ResultDisplay json.RawMessage `json:"resultDisplay"`
}

// transcriptConverter turns ChatRecords into the ACP updates that Qwen streams
// for a live child. It follows Qwen's own replay of a transcript
// (acp-bridge `transcript-replay.ts`) for the records a subagent writes.
type transcriptConverter struct {
	// promptSeen holds that the first user record, the spawn's prompt, went by.
	// The child's tab already opens on that prompt.
	promptSeen bool
	// callNames maps a tool call to its tool name, for a result record that
	// states none.
	callNames map[string]string
}

// convertedRecord is what one record becomes: messages that the parent sent to
// the child, and updates of the child's own.
type convertedRecord struct {
	userTexts []string
	updates   []json.RawMessage
}

func (c *transcriptConverter) convert(record chatRecord) convertedRecord {
	switch record.Type {
	case "user":
		return c.convertUser(record)
	case "assistant":
		return c.convertAssistant(record)
	case "tool_result":
		return c.convertToolResult(record)
	default:
		// A system record states telemetry, titles and checkpoints, which the
		// live stream never shows either.
		return convertedRecord{}
	}
}

// convertUser reads a message that the parent sent to the child. The first one
// is the spawn's prompt, which the child's tab already holds; a later one is a
// message that the parent sent to a running child.
func (c *transcriptConverter) convertUser(record chatRecord) convertedRecord {
	if record.Subtype != "" || record.Message == nil {
		return convertedRecord{}
	}
	var texts []string
	for _, part := range record.Message.Parts {
		if part.Text != nil && strings.TrimSpace(*part.Text) != "" {
			texts = append(texts, *part.Text)
		}
	}
	if len(texts) == 0 {
		return convertedRecord{}
	}
	if !c.promptSeen {
		c.promptSeen = true
		return convertedRecord{}
	}
	return convertedRecord{userTexts: []string{strings.Join(texts, "\n")}}
}

// convertAssistant reads the child's text, thinking and tool calls.
func (c *transcriptConverter) convertAssistant(record chatRecord) convertedRecord {
	if record.Message == nil {
		return convertedRecord{}
	}
	var out convertedRecord
	for _, part := range record.Message.Parts {
		switch {
		case part.FunctionCall != nil:
			call := part.FunctionCall
			if call.ID == "" {
				continue
			}
			if c.callNames == nil {
				c.callNames = make(map[string]string)
			}
			c.callNames[call.ID] = call.Name
			args := call.Args
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			out.updates = appendUpdate(out.updates, map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    call.ID,
				"status":        "pending",
				"title":         call.Name,
				"kind":          "other",
				"content":       []any{},
				"rawInput":      args,
				"_meta":         map[string]any{contracts.QwenMetaToolName: call.Name},
			})
		case part.Text != nil && *part.Text != "":
			updateType := "agent_message_chunk"
			if part.Thought {
				updateType = "agent_thought_chunk"
			}
			out.updates = appendUpdate(out.updates, map[string]any{
				"sessionUpdate": updateType,
				"content":       map[string]any{"type": "text", "text": *part.Text},
			})
		}
	}
	return out
}

// convertToolResult reads the result of one of the child's tool calls.
func (c *transcriptConverter) convertToolResult(record chatRecord) convertedRecord {
	var response *functionResponse
	if record.Message != nil {
		for _, part := range record.Message.Parts {
			if part.FunctionResponse != nil {
				response = part.FunctionResponse
				break
			}
		}
	}
	callID := ""
	if record.ToolCallResult != nil {
		callID = record.ToolCallResult.CallID
	}
	if callID == "" && response != nil {
		callID = response.ID
	}
	if callID == "" {
		return convertedRecord{}
	}
	name := c.callNames[callID]
	delete(c.callNames, callID)
	if name == "" && response != nil {
		name = response.Name
	}
	success := true
	var errorText string
	var display json.RawMessage
	if result := record.ToolCallResult; result != nil {
		errorText = rawText(result.Error)
		success = errorText == "" && (result.Status == "" || result.Status == "success")
		display = result.ResultDisplay
	}
	status := "completed"
	if !success {
		status = "failed"
	}
	update := map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    callID,
		"status":        status,
		"content":       toolResultContent(response, errorText, display),
		"_meta":         map[string]any{contracts.QwenMetaToolName: name},
	}
	if len(display) > 0 && string(display) != "null" {
		update["rawOutput"] = display
	}
	return convertedRecord{updates: appendUpdate(nil, update)}
}

// toolResultContent builds the content of a tool result as Qwen's replay does:
// the diff of an edit, else the error, else the text of the response.
func toolResultContent(response *functionResponse, errorText string, display json.RawMessage) []any {
	var edit struct {
		FileName        *string `json:"fileName"`
		FilePath        string  `json:"filePath"`
		OriginalContent string  `json:"originalContent"`
		NewContent      *string `json:"newContent"`
		Truncated       bool    `json:"truncatedForSession"`
	}
	if json.Unmarshal(display, &edit) == nil && edit.FileName != nil && edit.NewContent != nil && !edit.Truncated {
		path := edit.FilePath
		if path == "" {
			path = *edit.FileName
		}
		return []any{map[string]any{"type": "diff", "path": path, "oldText": edit.OriginalContent, "newText": *edit.NewContent}}
	}
	if errorText != "" {
		return []any{textBlock(errorText)}
	}
	if response == nil {
		return []any{}
	}
	if output := rawText(response.Response["output"]); output != "" {
		return []any{textBlock(output)}
	}
	if failure := rawText(response.Response["error"]); failure != "" {
		return []any{textBlock(failure)}
	}
	if len(response.Response) == 0 {
		return []any{}
	}
	encoded, err := json.Marshal(response.Response)
	if err != nil {
		return []any{}
	}
	return []any{textBlock(string(encoded))}
}

// textBlock is one text entry of a tool result's content.
func textBlock(text string) map[string]any {
	return map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}
}

// rawText reads a JSON string, or the `message` of a JSON error object. It
// returns "" for anything else.
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var withMessage struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &withMessage) == nil {
		return withMessage.Message
	}
	return ""
}

// appendUpdate encodes one update and appends it.
func appendUpdate(updates []json.RawMessage, update map[string]any) []json.RawMessage {
	encoded, err := json.Marshal(update)
	if err != nil {
		return updates
	}
	return append(updates, encoded)
}

// persistChildUserText writes a message that the parent sent to a running
// child into the child's transcript.
func (a *Agent) persistChildUserText(rowKey, text string) {
	childAgentID, _, found, err := a.Sink().LookupBackgroundTask(rowKey)
	if err != nil || !found || childAgentID == "" {
		return
	}
	if err := a.Sink().PersistChildUserMessage(childAgentID, text); err != nil {
		slog.Warn("qwen background subagent message persist failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
	}
}
