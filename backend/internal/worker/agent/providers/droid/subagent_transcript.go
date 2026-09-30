package droid

import (
	"bytes"
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
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Droid writes a child session to its own JSONL file while it runs. The
// stream JSON-RPC process only announces the child on the parent's stream.
// This reader follows that exact child's native file and writes its blocks to
// the child sink in file order.
const (
	droidArchivePollInterval = 250 * time.Millisecond
	droidArchiveReadLimit    = 1 << 20
	droidArchiveLineLimit    = 16 << 20
)

type droidChildTail struct {
	agent     *Agent
	sessionID string
	dataRoot  string
	workDir   string
	clock     quartz.Clock

	mu           sync.Mutex
	offset       int64
	partial      []byte
	skipping     bool
	headerSeen   bool
	fileInfo     os.FileInfo
	finished     bool
	invalid      bool
	pendingTurns int

	stopOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
}

func newDroidChildTail(a *Agent, sessionID, dataRoot string) *droidChildTail {
	clock := a.clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	return &droidChildTail{
		agent: a, sessionID: sessionID, dataRoot: dataRoot,
		workDir: a.workingDir, clock: clock,
		done: make(chan struct{}), stopped: make(chan struct{}),
	}
}

// startChildTail starts one reader for a native child that the registry linked.
func (a *Agent) startChildTail(sessionID string) {
	a.ensureChildTail(sessionID, false)
}

// resumeChildTail follows a send that the native child process accepted.
func (a *Agent) resumeChildTail(sessionID string) {
	a.ensureChildTail(sessionID, true)
}

func (a *Agent) ensureChildTail(sessionID string, continuation bool) {
	if a.beforeChildTailStart != nil {
		a.beforeChildTailStart()
	}
	if !filepath.IsAbs(a.workingDir) || !validDroidSessionID(sessionID) {
		return
	}
	dataRoot := droidDataRoot(agent.StoredSessionQuery{HomeDir: a.homeDir})
	if dataRoot == "" {
		return
	}
	a.tailMu.Lock()
	if a.tailClosing {
		a.tailMu.Unlock()
		return
	}
	if a.childTails == nil {
		a.childTails = make(map[string]*droidChildTail)
	}
	old := a.childTails[sessionID]
	if old != nil {
		if a.beforeChildTailStateRead != nil {
			a.beforeChildTailStateRead()
		}
		old.mu.Lock()
		if !old.finished || old.invalid {
			if continuation && !old.invalid {
				old.pendingTurns++
			}
			old.mu.Unlock()
			a.tailMu.Unlock()
			return
		}
	}
	tail := newDroidChildTail(a, sessionID, dataRoot)
	if old != nil {
		tail.offset = old.offset
		tail.headerSeen = old.headerSeen
		tail.fileInfo = old.fileInfo
		old.mu.Unlock()
	}
	a.childTails[sessionID] = tail
	if a.beforeChildTailRun != nil {
		a.beforeChildTailRun()
	}
	tail.start()
	a.tailMu.Unlock()
}

// markChildTailsClosing prevents a deferred notification from starting a tail
// after Stop starts.
func (a *Agent) markChildTailsClosing() {
	a.tailMu.Lock()
	a.tailClosing = true
	a.tailMu.Unlock()
}

func (a *Agent) stopChildTails() {
	a.tailMu.Lock()
	a.tailClosing = true
	tails := make([]*droidChildTail, 0, len(a.childTails))
	for _, tail := range a.childTails {
		tails = append(tails, tail)
	}
	a.tailMu.Unlock()
	for _, tail := range tails {
		tail.stop()
	}
}

func (t *droidChildTail) start() {
	ticker := t.clock.NewTicker(droidArchivePollInterval, "droid", "child-transcript")
	go func() {
		defer close(t.stopped)
		defer ticker.Stop()
		t.poll()
		for !t.isDone() {
			select {
			case <-t.done:
				return
			case <-t.agent.Context().Done():
				return
			case <-ticker.C:
				t.poll()
			}
		}
	}()
}

func (t *droidChildTail) stop() {
	t.stopOnce.Do(func() { close(t.done) })
	<-t.stopped
}

func (t *droidChildTail) isDone() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finished || t.invalid
}

// poll reads complete lines through a path confined to the Factory data root.
func (t *droidChildTail) poll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.invalid {
		return
	}
	for {
		chunk, err := t.read()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("droid: child archive read failed", "session_id", t.sessionID, "error", err)
				t.invalid = true
			}
			return
		}
		if len(chunk) == 0 {
			return
		}
		consumed := t.consume(chunk)
		t.offset += int64(consumed)
		if t.finished || t.invalid || len(chunk) < droidArchiveReadLimit {
			return
		}
	}
}

func validDroidSessionID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

// read rejects symlink escapes and file replacement before it reads new bytes.
func (t *droidChildTail) read() (data []byte, err error) {
	root, err := os.OpenRoot(t.dataRoot)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	project := filepath.Join(droidSessionsDirName, droidSanitizeCwd(t.workDir))
	for _, dir := range []string{droidSessionsDirName, project} {
		info, statErr := root.Lstat(dir)
		if statErr != nil {
			return nil, statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("droid child archive directory is not a regular directory: %s", dir)
		}
	}
	fileName := filepath.Join(project, t.sessionID+droidSessionSuffix)
	pathInfo, err := root.Lstat(fileName)
	if err != nil {
		return nil, err
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, errors.New("droid child archive is not a regular file")
	}
	file, err := root.Open(fileName)
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
	if !opened.Mode().IsRegular() || !os.SameFile(pathInfo, opened) ||
		(t.fileInfo != nil && !os.SameFile(t.fileInfo, opened)) {
		return nil, errors.New("droid child archive file changed identity")
	}
	if opened.Size() < t.offset {
		return nil, errors.New("droid child archive was truncated")
	}
	if t.fileInfo == nil {
		t.fileInfo = opened
	}
	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err = io.ReadAll(io.LimitReader(file, droidArchiveReadLimit))
	return data, err
}

func (t *droidChildTail) consume(chunk []byte) int {
	readBytes := len(chunk)
	for len(chunk) > 0 && !t.invalid && !t.finished {
		newline := bytes.IndexByte(chunk, '\n')
		if newline < 0 {
			if !t.skipping {
				t.partial = append(t.partial, chunk...)
				if len(t.partial) > droidArchiveLineLimit {
					t.partial = nil
					t.skipping = true
				}
			}
			return readBytes
		}
		line := chunk[:newline]
		chunk = chunk[newline+1:]
		if t.skipping {
			t.skipping = false
			continue
		}
		if len(t.partial)+len(line) > droidArchiveLineLimit {
			t.partial = nil
			slog.Warn("droid: child archive line exceeds limit", "session_id", t.sessionID)
			continue
		}
		if len(t.partial) > 0 {
			line = append(t.partial, line...)
			t.partial = nil
		}
		t.feed(bytes.TrimSpace(line))
	}
	return readBytes - len(chunk)
}

type droidArchiveRecord struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Reason  string `json:"reason"`
	Message struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

func (t *droidChildTail) feed(line []byte) {
	if len(line) == 0 {
		return
	}
	var record droidArchiveRecord
	if err := json.Unmarshal(line, &record); err != nil {
		if !t.headerSeen {
			t.invalid = true
		}
		slog.Warn("droid: child archive record unreadable", "session_id", t.sessionID, "error", err)
		return
	}
	if !t.headerSeen {
		if record.Type != droidSessionStartType || record.ID != t.sessionID || !sessionstore.SameDir(record.Cwd, t.workDir) {
			t.invalid = true
			slog.Warn("droid: child archive identity mismatch", "session_id", t.sessionID)
			return
		}
		t.headerSeen = true
		return
	}
	switch record.Type {
	case "message":
		t.projectMessage(record)
	case "agent_turn_outcome":
		continuation := t.pendingTurns > 0
		if continuation {
			t.pendingTurns--
		}
		t.projectTurnEnd(record.Reason, continuation)
		t.finished = !continuation
	}
}

func (t *droidChildTail) projectMessage(record droidArchiveRecord) {
	if strings.HasPrefix(record.ID, "context-") {
		return
	}
	for index, raw := range record.Message.Content {
		var block struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			slog.Warn("droid: child archive block unreadable", "session_id", t.sessionID, "error", err)
			continue
		}
		t.agent.dispatchMu.Lock()
		target, ok := t.agent.outputTargetFor(t.sessionID)
		if ok {
			switch {
			case record.Message.Role == "assistant" && block.Type == "text" && block.Text != "":
				t.persistAssembled(target, record.ID, index, agent.AssembledMessageKindText, block.Text)
			case record.Message.Role == "assistant" && block.Type == "thinking" && block.Thinking != "":
				t.persistAssembled(target, record.ID, index, agent.AssembledMessageKindReasoning, block.Thinking)
			case record.Message.Role == "assistant" && block.Type == "tool_use" && block.ID != "":
				t.projectToolCall(target, block.ID, block.Name, block.Input)
			case record.Message.Role == "user" && block.Type == "tool_result" && block.ToolUseID != "":
				t.projectToolResult(target, block.ToolUseID, block.Content, block.IsError)
			}
		}
		t.agent.dispatchMu.Unlock()
	}
}

func (t *droidChildTail) persistAssembled(target droidOutputTarget, messageID string, index int, kind agent.AssembledMessageKind, text string) {
	content, err := agent.MarshalAssembledMessage(kind, text, agent.MessageCompletionComplete)
	if err != nil {
		slog.Warn("droid: child archive text unreadable", "session_id", t.sessionID, "error", err)
		return
	}
	spanID := fmt.Sprintf("droid-msg-%s-%d", messageID, index)
	t.agent.persistRow(content, agent.SpanInfo{SpanID: spanID}, target)
}

func (t *droidChildTail) projectToolCall(target droidOutputTarget, id, name string, input json.RawMessage) {
	payload, err := json.Marshal(struct {
		Type    string       `json:"type"`
		ToolUse droidToolUse `json:"toolUse"`
	}{Type: "tool_call", ToolUse: droidToolUse{ID: id, Name: name, Input: input}})
	if err == nil {
		t.agent.onToolCall(payload, target)
	}
}

func (t *droidChildTail) projectToolResult(target droidOutputTarget, toolUseID string, content json.RawMessage, isError bool) {
	payload, err := json.Marshal(struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"toolUseId"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"isError"`
	}{Type: "tool_result", ToolUseID: toolUseID, Content: content, IsError: isError})
	if err == nil {
		t.agent.onToolResult(payload, target)
	}
}

func (t *droidChildTail) projectTurnEnd(reason string, continuation bool) {
	payload, err := json.Marshal(struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}{Type: "agent_turn_completed", Reason: reason})
	if err != nil {
		return
	}
	t.agent.dispatchMu.Lock()
	defer t.agent.dispatchMu.Unlock()
	target, ok := t.agent.outputTargetFor(t.sessionID)
	if !ok {
		return
	}
	_, status, exists, err := t.agent.sink.LookupBackgroundTask(t.sessionID)
	if err != nil || (exists && status.IsFinished()) {
		return
	}
	if continuation {
		t.agent.persistTurnEnd(payload, target)
	} else {
		t.agent.onAgentTurnCompleted(payload, target)
	}
}
