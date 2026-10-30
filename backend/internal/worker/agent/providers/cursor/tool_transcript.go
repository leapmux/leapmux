package cursor

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const cursorPendingTaskReportLimit = 256

type cursorTaskReportState struct {
	extensionSeen bool
	report        string
}

// cursorToolSource reads Cursor's tool records out of one session's own store.
type cursorToolSource struct {
	tooltranscript.SourceDefaults
	// store keeps the database handle and the blob index of the session's store file.
	// It carries its own mutex, which is what lets the supplement worker read it while
	// the reader goroutine resets it.
	store *cursorToolStore
	// resolveStorePath reports the store file of the session that runs now. The
	// transcript calls it for every agent message.
	resolveStorePath func() string
	// captureRecordObserver retains the session generation before the native read.
	// Cursor's ACP stream omits the subagent report, but its store keeps it in
	// the completed Task record.
	captureRecordObserver func() func(toolCallID string, record cursorToolRecord)
}

func newCursorToolTranscript(ctx context.Context, services agent.ProviderServices, storePath func() string) *tooltranscript.Transcript {
	return newCursorToolTranscriptWithObserver(ctx, services, storePath, nil)
}

func newCursorToolTranscriptWithObserver(
	ctx context.Context,
	services agent.ProviderServices,
	storePath func() string,
	captureObserver func() func(toolCallID string, record cursorToolRecord),
) *tooltranscript.Transcript {
	source := &cursorToolSource{store: &cursorToolStore{}, resolveStorePath: storePath, captureRecordObserver: captureObserver}
	// The store holds one database handle for the session's store file. The agent's
	// context ending is what releases it, because the transcript has no teardown call.
	context.AfterFunc(ctx, source.store.reset)
	return tooltranscript.New(ctx, services, source)
}

func (c *cursorToolSource) ProviderName() string { return "Cursor" }

func (c *cursorToolSource) Locate(string) tooltranscript.Location {
	path := c.resolveStorePath()
	return tooltranscript.Location{SessionKey: path, Path: path, Ready: path != ""}
}

func (c *cursorToolSource) ResetRecords() { c.store.reset() }

func (c *cursorToolSource) ToolCallID(original []byte) string { return acp.ToolCallID(original) }

func (c *cursorToolSource) ReadSupplements(ctx context.Context, location tooltranscript.Location, pending map[string]agent.MessageContent, _ bool) (map[string][]byte, error) {
	var observe func(string, cursorToolRecord)
	if c.captureRecordObserver != nil {
		observe = c.captureRecordObserver()
	}
	if observe != nil && (c.resolveStorePath == nil || c.resolveStorePath() != location.Path) {
		observe = nil
	}
	return readCursorToolSupplements(ctx, c.store, location.Path, pending, observe)
}

func (c *cursorToolSource) CaptureSupplement(location tooltranscript.Location, content agent.MessageContent, _ agent.SpanInfo) tooltranscript.SupplementReader {
	id := acp.ToolCallID(content.Original)
	if id == "" {
		return nil
	}
	store, path := c.store, location.Path
	pending := map[string]agent.MessageContent{id: content.Clone()}
	return func(ctx context.Context, _ bool) ([]byte, error) {
		records, err := readCursorToolSupplements(ctx, store, path, pending, nil)
		return records[id], err
	}
}

func readCursorToolSupplements(ctx context.Context, store *cursorToolStore, path string, pending map[string]agent.MessageContent, observe func(string, cursorToolRecord)) (map[string][]byte, error) {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	records, err := store.read(ctx, path, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(records))
	for id, record := range records {
		if observe != nil {
			observe(id, record)
		}
		supplement, err := cursorToolSupplement(pending[id].Original, record)
		if err != nil {
			return out, err
		}
		out[id] = supplement
	}
	return out, nil
}

func cursorToolSupplement(original []byte, record cursorToolRecord) ([]byte, error) {
	var tool map[string]json.RawMessage
	if err := json.Unmarshal(original, &tool); err != nil {
		return nil, err
	}
	supplement := acp.NewToolSupplement(tool)
	output := contracts.CursorStoredToolOutput{Content: []json.RawMessage{record.content}}
	// Keep the native record shape so the frontend owns tool-specific extraction.
	if providerOptions := record.result[contracts.CursorStoredToolProviderOptions]; len(providerOptions) > 0 {
		output.ProviderOptions = providerOptions
	}
	if len(record.arguments) > 0 {
		output.ToolArguments = record.arguments
	}
	if err := supplement.SetRawOutput(output); err != nil {
		return nil, err
	}
	return json.Marshal(supplement)
}

func (a *Agent) observeCursorTaskRecord(generation uint64, toolCallID string, record cursorToolRecord) {
	report := cursorTaskReport(record.content)
	if toolCallID == "" || report == "" {
		return
	}
	a.Mu.Lock()
	if generation != a.taskReportGeneration {
		a.Mu.Unlock()
		return
	}
	if _, native := a.nativeTasks[bgtask.NormalizeRowKey(toolCallID)]; native {
		a.Mu.Unlock()
		return
	}
	state := a.cursorTaskReportStateLocked(toolCallID)
	state.report = report
	a.taskReports[toolCallID] = state
	allowed := state.extensionSeen
	a.Mu.Unlock()
	if allowed {
		a.persistReadyCursorTaskReport(generation, toolCallID, report)
	}
}

func (a *Agent) noteCursorTaskExtension(toolCallID string) {
	a.Mu.Lock()
	if _, native := a.nativeTasks[bgtask.NormalizeRowKey(toolCallID)]; native {
		a.Mu.Unlock()
		return
	}
	generation := a.taskReportGeneration
	state := a.cursorTaskReportStateLocked(toolCallID)
	state.extensionSeen = true
	a.taskReports[toolCallID] = state
	report := state.report
	a.Mu.Unlock()
	if report != "" {
		a.persistReadyCursorTaskReport(generation, toolCallID, report)
	}
}

func (a *Agent) persistReadyCursorTaskReport(generation uint64, toolCallID, report string) {
	captured, present, err := agent.CaptureChildSubagentReport(a.Sink(), agent.ChildSubagentReportWrite{RowKey: bgtask.NormalizeRowKey(toolCallID), Write: agent.SubagentReportWrite{ReportID: toolCallID, Report: agent.SubagentReport{Label: "Cursor subagent", Text: report}}})
	if err != nil {
		slog.Warn("cursor task report capture failed", "agent_id", a.AgentID(), "tool_call_id", toolCallID, "error", err)
		return
	}
	if !present {
		return
	}
	expected := cursorTaskReportState{extensionSeen: true, report: report}
	a.Mu.Lock()
	current := generation == a.taskReportGeneration && a.taskReports[toolCallID] == expected
	if _, native := a.nativeTasks[bgtask.NormalizeRowKey(toolCallID)]; native {
		current = false
	}
	var batch *agent.CapturedTranscriptBatch
	if current {
		batch, err = agent.ReserveCapturedTranscriptBatch(captured.TranscriptWriter())
	}
	a.Mu.Unlock()
	if !current {
		return
	}
	if err != nil {
		slog.Warn("cursor task report admission failed", "agent_id", a.AgentID(), "tool_call_id", toolCallID, "error", err)
		return
	}
	err = batch.Prepare(func() []agent.CapturedTranscriptOperation {
		return []agent.CapturedTranscriptOperation{captured.NotificationOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)}
	})
	if !agent.OutputRetained(err) {
		slog.Warn("cursor task report persist failed", "agent_id", a.AgentID(), "tool_call_id", toolCallID, "error", err)
		return
	}
	a.Mu.Lock()
	if generation == a.taskReportGeneration && a.taskReports[toolCallID] == expected {
		a.taskReports[toolCallID] = cursorTaskReportState{extensionSeen: true}
	}
	a.Mu.Unlock()
}

func (a *Agent) cursorTaskReportStateLocked(toolCallID string) cursorTaskReportState {
	if a.taskReports == nil {
		a.taskReports = make(map[string]cursorTaskReportState)
	}
	if state, ok := a.taskReports[toolCallID]; ok {
		return state
	}
	if len(a.taskReports) >= cursorPendingTaskReportLimit {
		for candidate := range a.taskReports {
			delete(a.taskReports, candidate)
			break
		}
	}
	return cursorTaskReportState{}
}

func cursorTaskReport(content json.RawMessage) string {
	var block struct {
		ToolName string `json:"toolName"`
		Result   string `json:"result"`
	}
	if json.Unmarshal(content, &block) != nil || !strings.EqualFold(block.ToolName, contracts.CursorToolTask) {
		return ""
	}
	text := strings.TrimSpace(block.Result)
	const responseStart = "<response>"
	const responseEnd = "</response>"
	if _, after, ok := strings.Cut(text, responseStart); ok {
		if report, _, found := strings.Cut(after, responseEnd); found {
			return strings.TrimSpace(report)
		}
	}
	return text
}

func (a *Agent) captureCursorTaskRecordObserver() func(string, cursorToolRecord) {
	a.Mu.Lock()
	generation := a.taskReportGeneration
	a.Mu.Unlock()
	return func(id string, record cursorToolRecord) { a.observeCursorTaskRecord(generation, id, record) }
}
