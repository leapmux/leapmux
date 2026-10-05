package junie

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Junie replaces state.json after each state update. An offset reader would
// stay on the old file and lose later child messages.
const (
	junieChildPollInterval = 250 * time.Millisecond
	junieStateReadLimit    = 32 << 20
	junieSummaryReadLimit  = 1 << 20
	junieEventsReadLimit   = 64 << 20
)

var (
	errJunieStateNotReady = errors.New("junie child state is not ready")
	errJunieFileChanged   = errors.New("junie session file changed during read")
)

type junieStateSnapshot struct {
	Kind   string `json:"kind"`
	TaskID string `json:"taskId"`
	Event  struct {
		AgentEvent struct {
			Kind  string `json:"kind"`
			Blob  string `json:"blob"`
			Agent struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"agent"`
		} `json:"agentEvent"`
	} `json:"event"`
}

type junieStateBlob struct {
	LastAgentState struct {
		Subagents struct {
			Runs []junieStateRun `json:"runs"`
		} `json:"subagents"`
	} `json:"lastAgentState"`
}

type junieStateRun struct {
	Handle      string   `json:"handle"`
	TypeID      string   `json:"typeId"`
	DisplayName string   `json:"displayName"`
	Tasks       []string `json:"tasks"`
	Resume      struct {
		State struct {
			Observations []struct {
				Records []junieStateRecord `json:"records"`
			} `json:"observations"`
		} `json:"state"`
	} `json:"resume"`
}

type junieStateRecord struct {
	Request struct {
		Type       string `json:"type"`
		ToolCallID struct {
			ID     string `json:"id"`
			CallID string `json:"callId"`
			Name   string `json:"name"`
		} `json:"toolCallId"`
		InputParams struct {
			RawJSONObject json.RawMessage `json:"rawJsonObject"`
		} `json:"inputParams"`
	} `json:"request"`
	Result *junieStoredToolResult `json:"result"`
}

type junieStoredToolResult struct {
	Content string `json:"content"`
	Text    string `json:"text"`
	Images  []struct {
		ContentType string `json:"contentType"`
		Base64      string `json:"base64"`
	} `json:"images"`
}

// junieActionInProgressPlaceholder is the text that Junie stores as the result
// of a tool call that still runs. It replaces the text with the real result when
// the call ends (ACTION_IN_PROGRESS_PLACEHOLDER of AbstractIssueSingleStepAgentWorker).
// The two other placeholders of the jar, "The action was cancelled." and "The
// action was interrupted because the user sent a real-time follow-up message.",
// state a call that ended, so they stay results.
const junieActionInProgressPlaceholder = "The action is in progress."

// isActionInProgress reports whether the stored result is the placeholder of a
// call that still runs, and no real result yet.
func (r *junieStoredToolResult) isActionInProgress() bool {
	if r == nil || len(r.Images) > 0 {
		return false
	}
	return r.text() == junieActionInProgressPlaceholder
}

// text returns the text of the result. Junie states it in `text`, and in
// `content` when it states no `text`.
func (r *junieStoredToolResult) text() string {
	if r.Text != "" {
		return r.Text
	}
	return r.Content
}

type junieToolRecord struct {
	callID string
	name   string
	input  json.RawMessage
	result *junieStoredToolResult
}

type junieStoredSummary struct {
	SessionID  string `json:"sessionId"`
	ProjectDir string `json:"projectDir"`
	Subagents  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"subagents"`
}

type junieStoredEvent struct {
	Kind   string `json:"kind"`
	TaskID string `json:"taskId"`
	Event  struct {
		AgentEvent struct {
			Kind   string `json:"kind"`
			StepID string `json:"stepId"`
			Name   string `json:"name"`
			Task   string `json:"task"`
			Agent  struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"agent"`
		} `json:"agentEvent"`
	} `json:"event"`
}

type junieChildLink struct {
	handle string
	taskID string
}

// junieChildEventLink joins the ACP child session to the native run handle.
// The ACP id uses the custom-agent step UUID; state.json uses agent-<number>.
// Two events with the same task ID carry the bridge between them.
func junieChildEventLink(data []byte, childSessionID, childName, prompt string) (junieChildLink, error) {
	stepID, ok := junieChildStepID(childSessionID)
	if !ok || childName == "" || prompt == "" {
		return junieChildLink{}, errors.New("junie child event identity is incomplete")
	}
	var spawnTaskID, updateTaskID, handle string
	lines := bytes.Split(data, []byte{'\n'})
	// Junie's event store appends JSONL. Its final line can be unfinished while
	// the writer works, so only newline-terminated records count.
	for _, line := range lines[:len(lines)-1] {
		if !bytes.Contains(line, []byte("SubagentSpawnedEvent")) &&
			!bytes.Contains(line, []byte("CustomAgentBlockUpdatedEvent")) {
			continue
		}
		var event junieStoredEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return junieChildLink{}, fmt.Errorf("parse junie child event: %w", err)
		}
		if event.Kind != "SessionA2uxEvent" || event.TaskID == "" {
			continue
		}
		child := event.Event.AgentEvent
		switch {
		case child.Kind == "SubagentSpawnedEvent" && child.StepID == stepID+"-spawned":
			if child.Agent.Kind != "MainAgent" || child.Agent.ID != "main" ||
				child.Name != childName || child.Task != prompt ||
				(spawnTaskID != "" && spawnTaskID != event.TaskID) {
				return junieChildLink{}, errors.New("junie spawn event differs from the ACP child")
			}
			spawnTaskID = event.TaskID
		case child.Kind == "CustomAgentBlockUpdatedEvent" && child.StepID == stepID:
			if child.Agent.Kind != "CustomAgent" || !safeJunieHandle(child.Agent.ID) ||
				child.Agent.Name != childName || child.Name != childName ||
				(updateTaskID != "" && (updateTaskID != event.TaskID || handle != child.Agent.ID)) {
				return junieChildLink{}, errors.New("junie child event has an ambiguous run handle")
			}
			updateTaskID = event.TaskID
			handle = child.Agent.ID
		}
	}
	if spawnTaskID == "" || updateTaskID == "" {
		return junieChildLink{}, errJunieStateNotReady
	}
	if spawnTaskID != updateTaskID {
		return junieChildLink{}, errors.New("junie child events belong to different tasks")
	}
	return junieChildLink{handle: handle, taskID: spawnTaskID}, nil
}

func junieChildStepID(childSessionID string) (string, bool) {
	stepID, ok := strings.CutPrefix(childSessionID, "subagent-")
	if !ok {
		return "", false
	}
	parsed, err := uuid.Parse(stepID)
	return stepID, err == nil && parsed.String() == stepID
}

func safeJunieHandle(handle string) bool {
	return strings.HasPrefix(handle, "agent-") && len(handle) > len("agent-") &&
		filepath.Base(handle) == handle && !strings.ContainsAny(handle, `/\`)
}

// junieChildToolRecords selects one child run by the native session link and
// the exact task. A wrong child can have the same display name, so the handle
// and task must both match before any bytes reach a child tab.
func junieChildToolRecords(data []byte, link junieChildLink, childName, prompt string) ([]junieToolRecord, error) {
	if !safeJunieHandle(link.handle) || link.taskID == "" || childName == "" || prompt == "" {
		return nil, errors.New("junie child identity is incomplete")
	}
	var snapshot junieStateSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("%w: parse state: %v", errJunieStateNotReady, err)
	}
	if snapshot.Kind != "SessionA2uxEvent" || snapshot.TaskID != link.taskID ||
		snapshot.Event.AgentEvent.Kind != "AgentStateUpdatedEvent" ||
		snapshot.Event.AgentEvent.Agent.Kind != "MainAgent" ||
		snapshot.Event.AgentEvent.Agent.ID != "main" {
		return nil, errors.New("junie state is not a main-agent snapshot")
	}
	if snapshot.Event.AgentEvent.Blob == "" {
		return nil, errJunieStateNotReady
	}
	var blob junieStateBlob
	if err := json.Unmarshal([]byte(snapshot.Event.AgentEvent.Blob), &blob); err != nil {
		return nil, fmt.Errorf("%w: parse state blob: %v", errJunieStateNotReady, err)
	}
	var selected *junieStateRun
	for i := range blob.LastAgentState.Subagents.Runs {
		run := &blob.LastAgentState.Subagents.Runs[i]
		if run.Handle != link.handle {
			continue
		}
		if selected != nil {
			return nil, errors.New("multiple junie child runs have the same handle")
		}
		selected = run
	}
	if selected == nil {
		return nil, errJunieStateNotReady
	}
	if selected.TypeID != childName || selected.DisplayName != childName ||
		len(selected.Tasks) == 0 || selected.Tasks[0] != prompt {
		return nil, errors.New("junie child state differs from its ACP announcement")
	}
	var out []junieToolRecord
	for _, observation := range selected.Resume.State.Observations {
		for _, record := range observation.Records {
			if !strings.HasSuffix(record.Request.Type, ".ToolActionRequest") {
				continue
			}
			call := record.Request.ToolCallID
			if call.CallID == "" || call.Name == "" {
				return nil, errors.New("junie child tool identity is incomplete")
			}
			input := record.Request.InputParams.RawJSONObject
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			if !json.Valid(input) {
				return nil, errors.New("junie child tool input is not JSON")
			}
			// A call that still runs has no result yet. The placeholder must not close
			// the row, because the real result replaces it in a later snapshot.
			result := record.Result
			if result.isActionInProgress() {
				result = nil
			}
			out = append(out, junieToolRecord{callID: call.CallID, name: call.Name, input: input, result: result})
		}
	}
	return out, nil
}

func safeJunieSessionID(sessionID string) bool {
	return strings.HasPrefix(sessionID, "session-") && filepath.Base(sessionID) == sessionID &&
		!strings.ContainsAny(sessionID, `/\`) && sessionID != "session-"
}

// readJunieSummary checks the native session and child backlink before the
// reader sends stored bytes to a child tab. A finished child can disappear
// from a later summary; a link this reader already checked stays valid.
func readJunieSummary(home, sessionID, workingDir, handle, childName string, linkedBefore bool) (bool, error) {
	data, err := readJunieRegularFile(home, sessionID, "summary.json", junieSummaryReadLimit)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errJunieFileChanged) {
		return false, errJunieStateNotReady
	}
	if err != nil {
		return false, err
	}
	var summary junieStoredSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return false, fmt.Errorf("%w: parse summary: %v", errJunieStateNotReady, err)
	}
	if summary.SessionID != sessionID || (workingDir != "" && summary.ProjectDir != workingDir) {
		return false, errors.New("junie summary identifies another session or project")
	}
	linked := false
	for _, child := range summary.Subagents {
		if child.ID != handle {
			continue
		}
		if child.Name != childName || linked {
			return false, errors.New("junie summary has an ambiguous child link")
		}
		linked = true
	}
	if !linked && !linkedBefore {
		return false, errJunieStateNotReady
	}
	return true, nil
}

// readJunieEvents reads the append-only link between an ACP step UUID and the
// native child handle. The child reader spends this file once per child.
func readJunieEvents(home, sessionID string) ([]byte, error) {
	return readJunieRegularFile(home, sessionID, "events.jsonl", junieEventsReadLimit)
}

// readJunieState reads one native snapshot after its summary links the child.
func readJunieState(home, sessionID string) ([]byte, error) {
	return readJunieRegularFile(home, sessionID, "state.json", junieStateReadLimit)
}

// readJunieRegularFile reads one native file through a confined root. Junie's
// atomic writer can replace a file between Lstat and Open. That change asks
// the caller to retry; it is not a damaged archive.
func readJunieRegularFile(home, sessionID, fileName string, limit int64) (data []byte, err error) {
	if !filepath.IsAbs(home) || !safeJunieSessionID(sessionID) {
		return nil, errors.New("junie state path is invalid")
	}
	if fileName != "state.json" && fileName != "summary.json" && fileName != "events.jsonl" {
		return nil, errors.New("junie session file name is invalid")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	for _, directory := range []string{"sessions", filepath.Join("sessions", sessionID)} {
		info, statErr := root.Lstat(directory)
		if statErr != nil {
			return nil, statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("junie state directory is not a regular directory")
		}
	}
	name := filepath.Join("sessions", sessionID, fileName)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("junie session file is not a regular file within the size cap")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errJunieFileChanged
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("junie session file exceeds the size cap")
	}
	return data, nil
}

type junieToolPhase struct {
	request bool
	result  bool
}

// junieChildTail polls replacement snapshots and emits each tool phase once.
type junieChildTail struct {
	agent          *Agent
	childSessionID string
	childName      string
	prompt         string
	rootSessionID  string
	home           string
	workingDir     string
	clock          quartz.Clock

	mu            sync.Mutex
	seen          map[string]junieToolPhase
	eventLink     *junieChildLink
	summaryLinked bool
	invalid       bool

	stopOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
}

func newJunieChildTail(a *Agent, rootSessionID, childSessionID, childName, prompt string) *junieChildTail {
	clock := a.clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	return &junieChildTail{
		agent: a, childSessionID: childSessionID, childName: childName, prompt: prompt,
		rootSessionID: rootSessionID,
		home:          junieHome(agent.StoredSessionQuery{HomeDir: a.homeDir}),
		workingDir:    a.workingDir,
		clock:         clock,
		seen:          make(map[string]junieToolPhase),
		done:          make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (a *Agent) startChildTail(childSessionID, childName, prompt string) {
	rootSessionID := a.CurrentSessionID()
	if !safeJunieSessionID(rootSessionID) || !filepath.IsAbs(junieHome(agent.StoredSessionQuery{HomeDir: a.homeDir})) {
		return
	}
	a.childTailMu.Lock()
	defer a.childTailMu.Unlock()
	if a.childTails == nil {
		a.childTails = make(map[string]*junieChildTail)
	}
	if a.childTails[childSessionID] != nil {
		return
	}
	tail := newJunieChildTail(a, rootSessionID, childSessionID, childName, prompt)
	tail.start()
	a.childTails[childSessionID] = tail
}

func (a *Agent) finishChildTail(childSessionID string) {
	a.childTailMu.Lock()
	tail := a.childTails[childSessionID]
	delete(a.childTails, childSessionID)
	a.childTailMu.Unlock()
	if tail != nil {
		tail.stop()
		tail.poll()
	}
}

func (a *Agent) stopChildTails() {
	a.childTailMu.Lock()
	tails := a.childTails
	a.childTails = nil
	a.childTailMu.Unlock()
	for _, tail := range tails {
		tail.stop()
	}
}

func (t *junieChildTail) start() {
	t.poll()
	ticker := t.clock.NewTicker(junieChildPollInterval, "junie", "child-state")
	ctx := t.agent.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer close(t.stopped)
		defer ticker.Stop()
		for {
			select {
			case <-t.done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.poll()
			}
		}
	}()
}

func (t *junieChildTail) stop() {
	t.stopOnce.Do(func() { close(t.done) })
	<-t.stopped
}

func (t *junieChildTail) poll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.invalid {
		return
	}
	if t.eventLink == nil {
		events, err := readJunieEvents(t.home, t.rootSessionID)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errJunieFileChanged) {
			return
		}
		if err != nil {
			slog.Warn("junie child events read failed", "agent_id", t.agent.AgentID(), "child_session_id", t.childSessionID, "error", err)
			t.invalid = true
			return
		}
		link, err := junieChildEventLink(events, t.childSessionID, t.childName, t.prompt)
		if errors.Is(err, errJunieStateNotReady) {
			return
		}
		if err != nil {
			slog.Warn("junie child event link failed", "agent_id", t.agent.AgentID(), "child_session_id", t.childSessionID, "error", err)
			t.invalid = true
			return
		}
		t.eventLink = &link
	}
	linked, err := readJunieSummary(t.home, t.rootSessionID, t.workingDir, t.eventLink.handle, t.childName, t.summaryLinked)
	if errors.Is(err, errJunieStateNotReady) {
		return
	}
	if err != nil {
		slog.Warn("junie child summary link failed", "agent_id", t.agent.AgentID(), "child_session_id", t.childSessionID, "error", err)
		t.invalid = true
		return
	}
	t.summaryLinked = linked
	data, err := readJunieState(t.home, t.rootSessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errJunieFileChanged) {
			return
		}
		slog.Warn("junie child state read failed", "agent_id", t.agent.AgentID(), "child_session_id", t.childSessionID, "error", err)
		t.invalid = true
		return
	}
	records, err := junieChildToolRecords(data, *t.eventLink, t.childName, t.prompt)
	if errors.Is(err, errJunieStateNotReady) {
		return
	}
	if err != nil {
		slog.Warn("junie child state identity failed", "agent_id", t.agent.AgentID(), "child_session_id", t.childSessionID, "error", err)
		t.invalid = true
		return
	}
	for _, record := range records {
		phase := t.seen[record.callID]
		if !phase.request {
			update, encodeErr := junieToolRequestUpdate(record)
			if encodeErr != nil || !t.agent.FeedChildUpdate(t.childSessionID, update) {
				return
			}
			phase.request = true
		}
		if !phase.result && record.result != nil {
			update, encodeErr := junieToolResultUpdate(record)
			if encodeErr != nil || !t.agent.FeedChildUpdate(t.childSessionID, update) {
				t.seen[record.callID] = phase
				return
			}
			phase.result = true
		}
		t.seen[record.callID] = phase
	}
}

func junieToolRequestUpdate(record junieToolRecord) ([]byte, error) {
	return json.Marshal(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": record.callID,
		"title": record.name, "kind": junieToolKind(record.name),
		"status": "in_progress", "rawInput": record.input,
	})
}

func junieToolResultUpdate(record junieToolRecord) ([]byte, error) {
	var content []map[string]any
	if text := record.result.text(); text != "" {
		content = append(content, map[string]any{
			"type": "content", "content": map[string]string{"type": "text", "text": text},
		})
	}
	for _, image := range record.result.Images {
		if !strings.HasPrefix(image.ContentType, "image/") || image.Base64 == "" {
			continue
		}
		if _, err := base64.StdEncoding.DecodeString(image.Base64); err != nil {
			continue
		}
		content = append(content, map[string]any{
			"type": "content", "content": map[string]string{
				"type": "image", "mimeType": image.ContentType, "data": image.Base64,
			},
		})
	}
	return json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": record.callID,
		"title": record.name, "kind": junieToolKind(record.name),
		"status": "completed", "content": content,
	})
}

func junieToolKind(name string) string {
	switch name {
	case "open_entire_file", "scroll_down", "scroll_up", "glob_search", "grep_search":
		return "read"
	case "bash":
		return "execute"
	case "search_replace", "multi_edit", "create", "undo_edit":
		return "edit"
	default:
		return "other"
	}
}
