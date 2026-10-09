package dirac

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	diracTaskIndexLimit       = 4 << 20
	diracTranscriptLimit      = 16 << 20
	diracTaskHistoryLimit     = 4 << 20
	diracArchiveRetryInterval = 250 * time.Millisecond
	diracArchiveRetryMax      = 4 * time.Second
	diracArchiveDeadline      = 2 * time.Minute
	diracArchiveTimerTag      = "dirac-child-archive"
)

type diracArchiveIndexRecord struct {
	TaskID     string `json:"taskId"`
	RunID      string `json:"runId"`
	Transcript string `json:"transcript"`
	Status     string `json:"status"`
	Agent      struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
}

type diracArchiveHeader struct {
	TaskID string `json:"taskId"`
	RunID  string `json:"runId"`
	Prompt string `json:"prompt"`
	Agent  struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
}

type diracArchiveEvent struct {
	Kind    string
	Details json.RawMessage
}

// beforeWaitCleanup reads a final child that exited before PromptEnded ran.
func (a *Agent) beforeWaitCleanup() {
	a.hydrateSubagentArchives(nil, true)
	a.archiveWG.Wait()
}

// hydrateSubagentArchives gives completed children an immediate archive read.
func (a *Agent) hydrateSubagentArchives(_ error, _ bool) {
	a.childMu.Lock()
	var pending []*diracChildState
	for _, state := range a.childState {
		if state.finalSeen {
			pending = append(pending, state)
		}
	}
	a.childMu.Unlock()
	for _, state := range pending {
		if obs := a.replayDiracChild(state); obs != nil {
			a.ApplySubagentObservation(obs)
		} else {
			a.startDiracArchiveRetry(state)
		}
	}
}

func (a *Agent) archiveClock() quartz.Clock {
	if a.clock != nil {
		return a.clock
	}
	return quartz.NewReal()
}

// clearChildState ends archive reads of the outgoing native session.
func (a *Agent) clearChildState() {
	a.childMu.Lock()
	if a.childStop != nil {
		close(a.childStop)
		a.childStop = nil
	}
	a.childState = nil
	a.childDone = nil
	a.childMu.Unlock()
}

// replayDiracChild advances only after the child route accepts an update.
func (a *Agent) replayDiracChild(state *diracChildState) *acp.SubagentObservation {
	state.replayMu.Lock()
	defer state.replayMu.Unlock()
	if !a.diracChildCurrent(state) {
		return nil
	}
	events, err := readDiracChildArchive(a.root, state.sessionID, state)
	if err != nil {
		a.noteDiracArchiveError(state, err)
		return nil
	}
	for state.eventIndex < len(events) {
		update, encodeErr := diracArchiveUpdate(events[state.eventIndex])
		if encodeErr != nil {
			a.noteDiracArchiveError(state, encodeErr)
			return nil
		}
		if !a.diracChildCurrent(state) {
			return nil
		}
		if update != nil && !a.FeedChildUpdate(state.rowKey, update) {
			a.noteDiracArchiveError(state, fmt.Errorf("child route refused archive event %d", state.eventIndex))
			return nil
		}
		state.eventIndex++
	}
	if !a.finishDiracChild(state) {
		return nil
	}
	status := state.finalStatus
	if status == bgtask.StatusUnspecified {
		status = bgtask.StatusSucceeded
	}
	return &acp.SubagentObservation{
		RowKey: state.rowKey, Status: status, CloseRow: true, Mode: acp.ModeCloseOnly,
	}
}

func (a *Agent) diracChildCurrent(state *diracChildState) bool {
	a.childMu.Lock()
	current := a.childState[state.nativeID] == state
	a.childMu.Unlock()
	return current && a.CurrentSessionID() == state.sessionID
}

func (a *Agent) finishDiracChild(state *diracChildState) bool {
	a.childMu.Lock()
	defer a.childMu.Unlock()
	if a.CurrentSessionID() != state.sessionID || a.childState[state.nativeID] != state {
		return false
	}
	delete(a.childState, state.nativeID)
	if a.childDone == nil {
		a.childDone = make(map[string]struct{})
	}
	a.childDone[state.nativeID] = struct{}{}
	close(state.done)
	return true
}

func (a *Agent) noteDiracArchiveError(state *diracChildState, err error) {
	if state.lastError == nil || state.lastError.Error() != err.Error() {
		slog.Warn("dirac child archive replay failed", "agent_id", a.AgentID(), "row_key", state.rowKey, "error", err)
	}
	state.lastError = err
}

func (a *Agent) startDiracArchiveRetry(state *diracChildState) {
	a.childMu.Lock()
	if a.childState[state.nativeID] != state || state.retrying {
		a.childMu.Unlock()
		return
	}
	if a.childStop == nil {
		a.childStop = make(chan struct{})
	}
	state.retrying = true
	stop := a.childStop
	a.archiveWG.Add(1)
	a.childMu.Unlock()
	go a.retryDiracChild(state, stop)
}

func (a *Agent) retryDiracChild(state *diracChildState, stop <-chan struct{}) {
	defer a.archiveWG.Done()
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	clock := a.archiveClock()
	delay := diracArchiveRetryInterval
	for {
		remaining := state.deadline.Sub(clock.Now())
		if remaining <= 0 {
			if obs := a.failDiracChildArchive(state, "Child transcript unavailable after two minutes"); obs != nil {
				a.ApplySubagentObservation(obs)
			}
			return
		}
		timer := clock.NewTimer(min(delay, remaining), diracArchiveTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(diracArchiveTimerTag)
			if obs := a.failDiracChildArchive(state, "process exited before the child archive was ready"); obs != nil {
				a.ApplySubagentObservation(obs)
			}
			return
		case <-stop:
			timer.Stop(diracArchiveTimerTag)
			return
		case <-state.done:
			timer.Stop(diracArchiveTimerTag)
			return
		case <-timer.C:
		}
		if obs := a.replayDiracChild(state); obs != nil {
			a.ApplySubagentObservation(obs)
			return
		}
		if !clock.Now().Before(state.deadline) {
			if obs := a.failDiracChildArchive(state, "Child transcript unavailable after two minutes"); obs != nil {
				a.ApplySubagentObservation(obs)
			}
			return
		}
		delay = min(delay*2, diracArchiveRetryMax)
	}
}

func (a *Agent) failDiracChildArchive(state *diracChildState, reason string) *acp.SubagentObservation {
	state.replayMu.Lock()
	defer state.replayMu.Unlock()
	if !a.diracChildCurrent(state) || !a.finishDiracChild(state) {
		return nil
	}
	detail := "the archive did not become readable"
	if state.lastError != nil {
		detail = state.lastError.Error()
	}
	message := reason + ": " + detail
	return &acp.SubagentObservation{
		RowKey: state.rowKey, Title: state.agentName, Status: bgtask.StatusFailed, CloseRow: true,
		ReportID: state.rowKey,
		Report:   agent.SubagentReport{Label: "Archive unavailable", Text: message, Status: "failed"},
	}
}

// readDiracChildArchive resolves a child inside the current session's task.
func readDiracChildArchive(root, sessionID string, child *diracChildState) (archive []diracArchiveEvent, err error) {
	archiveRoot, err := sessionstore.OpenArchiveRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open dirac archive root: %w", err)
	}
	return readDiracChildArchiveFromRoot(archiveRoot, sessionID, child)
}

func readDiracChildArchiveFromRoot(archiveRoot sessionstore.ArchiveRoot, sessionID string, child *diracChildState) (archive []diracArchiveEvent, err error) {
	defer func() {
		if closeErr := archiveRoot.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	dataChain, err := sessionstore.OpenCheckedArchiveDirectoryChain(archiveRoot, "data")
	if err != nil {
		return nil, fmt.Errorf("open dirac archive data: %w", err)
	}
	defer func() {
		if closeErr := dataChain.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	dataRoot := dataChain.Root()
	tasksChain, err := sessionstore.OpenCheckedArchiveDirectoryChain(dataRoot, "tasks")
	if err != nil {
		return nil, fmt.Errorf("open dirac task archives: %w", err)
	}
	defer func() {
		if closeErr := tasksChain.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	tasksRoot := tasksChain.Root()
	taskID, err := diracTaskForSession(dataRoot, tasksRoot, sessionID)
	if err != nil {
		return nil, err
	}
	taskChain, err := sessionstore.OpenCheckedArchiveDirectoryChain(tasksRoot, taskID, "subagents")
	if err != nil {
		return nil, fmt.Errorf("open dirac subagent archive: %w", err)
	}
	defer func() {
		if closeErr := taskChain.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	subagentsRoot := taskChain.Root()
	indexRaw, err := readDiracRegularFile(subagentsRoot, "index.md", diracTaskIndexLimit)
	if err != nil {
		return nil, fmt.Errorf("read dirac subagent index: %w", err)
	}
	indexBlocks, err := diracJSONBlocks(indexRaw)
	if err != nil {
		return nil, fmt.Errorf("parse dirac subagent index: %w", err)
	}
	for _, block := range indexBlocks {
		var record diracArchiveIndexRecord
		if json.Unmarshal(block, &record) != nil || record.TaskID != taskID ||
			record.Agent.ID != child.agentID || record.Agent.Name != child.agentName ||
			record.Status == "started" {
			continue
		}
		if !safeDiracComponent(record.RunID) || record.Transcript != filepath.Join(record.RunID, "transcript.md") {
			return nil, errors.New("the dirac subagent index has an unsafe transcript path")
		}
		transcript, err := readDiracRunTranscript(subagentsRoot, record.RunID)
		if err != nil {
			return nil, fmt.Errorf("read dirac subagent transcript: %w", err)
		}
		headers, err := diracJSONBlocks(transcript)
		if err != nil {
			return nil, fmt.Errorf("parse dirac subagent transcript: %w", err)
		}
		if len(headers) == 0 {
			return nil, errors.New("the dirac subagent transcript has no identity header")
		}
		var header diracArchiveHeader
		if json.Unmarshal(headers[0], &header) != nil || header.TaskID != taskID ||
			header.RunID != record.RunID || header.Agent.ID != child.agentID ||
			header.Agent.Name != child.agentName || header.Prompt != child.prompt {
			return nil, errors.New("the dirac subagent transcript identity differs from its card")
		}
		if archive != nil {
			return nil, errors.New("multiple dirac subagent runs match one child card")
		}
		archive, err = diracArchiveEvents(transcript)
		if err != nil {
			return nil, fmt.Errorf("parse dirac child events: %w", err)
		}
	}
	if archive == nil {
		return nil, errors.New("no completed dirac archive matches the child card")
	}
	return archive, nil
}

func readDiracRunTranscript(root sessionstore.ArchiveRoot, runID string) (data []byte, err error) {
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			data, err = nil, errors.Join(err, closeErr)
		}
	}()
	return readDiracRegularFile(chain.Root(), "transcript.md", diracTranscriptLimit)
}

// diracArchiveEvents preserves the order of the native transcript events.
func diracArchiveEvents(data []byte) ([]diracArchiveEvent, error) {
	blocks, err := diracJSONBlocks(data)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, errors.New("the dirac transcript has no header")
	}
	var kinds []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), diracTranscriptLimit)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") && strings.Contains(line, " · event ") {
			at := strings.LastIndex(line, " · ")
			if at >= 0 {
				kinds = append(kinds, line[at+len(" · "):])
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(kinds)+1 != len(blocks) {
		return nil, errors.New("the dirac transcript event count differs from its data blocks")
	}
	events := make([]diracArchiveEvent, 0, len(kinds))
	for i, kind := range kinds {
		events = append(events, diracArchiveEvent{Kind: kind, Details: blocks[i+1]})
	}
	return events, nil
}

// diracArchiveUpdate translates one stored event into a child chat row.
func diracArchiveUpdate(event diracArchiveEvent) ([]byte, error) {
	textUpdate := func(value string) ([]byte, error) {
		if value == "" {
			return nil, nil
		}
		return json.Marshal(map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]string{"type": "text", "text": value},
		})
	}
	switch event.Kind {
	case "assistant_text", "progress":
		var details struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Details, &details); err != nil {
			return nil, err
		}
		return textUpdate(details.Text)
	case "tool_call", "tool_result":
		var details struct {
			ToolUseID string          `json:"toolUseId"`
			ID        string          `json:"id"`
			CallID    string          `json:"callId"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Result    json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(event.Details, &details); err != nil {
			return nil, err
		}
		callID := details.ToolUseID
		if callID == "" {
			callID = details.CallID
		}
		if callID == "" {
			callID = details.ID
		}
		if callID == "" {
			return nil, errors.New("the dirac archive tool has no call id")
		}
		if event.Kind == "tool_call" {
			return json.Marshal(map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": callID,
				"title": details.Name, "kind": "other", "status": "in_progress", "rawInput": details.Input,
			})
		}
		result := strings.TrimSpace(string(details.Result))
		var plain string
		if json.Unmarshal(details.Result, &plain) == nil {
			result = plain
		}
		return json.Marshal(map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": callID,
			"status": "completed", "rawOutput": details.Result,
			"content": []any{map[string]any{
				"type": "content", "content": map[string]string{"type": "text", "text": result},
			}},
		})
	case "terminal":
		var details struct {
			Result string `json:"result"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(event.Details, &details); err != nil {
			return nil, err
		}
		if details.Result != "" {
			return textUpdate(details.Result)
		}
		return textUpdate(details.Error)
	case "usage":
		return nil, nil
	default:
		return textUpdate(event.Kind + ": " + string(event.Details))
	}
}

func diracTaskForSession(dataRoot, tasksRoot sessionstore.ArchiveRoot, sessionID string) (string, error) {
	if !safeDiracComponent(sessionID) {
		return "", errors.New("the dirac session id is invalid")
	}
	mapRaw, err := readDiracRegularFile(dataRoot, "acp-session-tasks.json", diracTaskHistoryLimit)
	if err == nil {
		var replacements map[string][]string
		if json.Unmarshal(mapRaw, &replacements) == nil {
			tasks := replacements[sessionID]
			if len(tasks) > 0 && safeDiracComponent(tasks[len(tasks)-1]) {
				return tasks[len(tasks)-1], nil
			}
		}
	}
	historyRaw, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(dataRoot, diracTaskHistoryLimit, "state", "taskHistory.json")
	if err == nil {
		var history []diracHistoryRecord
		if json.Unmarshal(historyRaw, &history) == nil {
			var latest diracHistoryRecord
			for _, record := range history {
				if record.ULID == sessionID && safeDiracComponent(record.ID) && record.TS >= latest.TS {
					latest = record
				}
			}
			if latest.ID != "" {
				return latest.ID, nil
			}
		}
	}
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(tasksRoot, sessionID)
	if err == nil {
		if closeErr := chain.Close(); closeErr != nil {
			return "", fmt.Errorf("close dirac task archive: %w", closeErr)
		}
		return sessionID, nil
	}
	return "", errors.New("no dirac task belongs to the ACP session")
}

func safeDiracComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\`)
}

type diracArchiveFileOpener interface {
	Lstat(string) (os.FileInfo, error)
	Open(string) (*os.File, error)
}

func readDiracRegularFile(opener diracArchiveFileOpener, path string, limit int64) ([]byte, error) {
	data, err := sessionstore.ReadRegularFile(opener, path, limit)
	if err != nil {
		return nil, fmt.Errorf("read the dirac archive file: %w", err)
	}
	return data, nil
}

// diracJSONBlocks reads the JSON fences in Dirac's append-only Markdown store.
func diracJSONBlocks(data []byte) ([]json.RawMessage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), diracTranscriptLimit)
	var blocks []json.RawMessage
	var fence string
	var body strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if fence == "" {
			candidate := strings.TrimSuffix(line, "json")
			if strings.HasSuffix(line, "json") && len(candidate) >= 3 && strings.Trim(candidate, "`") == "" {
				fence = candidate
				body.Reset()
			}
			continue
		}
		if line == fence {
			blocks = append(blocks, json.RawMessage(body.String()))
			fence = ""
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return blocks, nil
}
