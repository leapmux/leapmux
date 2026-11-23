package droid

import (
	"bufio"
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
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

// droidArchivePosition holds a validated pre-send archive offset so a resumed
// tail skips earlier turns without replaying them.
type droidArchivePosition struct {
	offset     int64
	headerSeen bool
	fileInfo   os.FileInfo
}

type droidChildTail struct {
	agent     *Agent
	sessionID string
	dataRoot  string
	workDir   string
	clock     quartz.Clock
	ready     <-chan struct{}

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

func newDroidChildTail(a *Agent, sessionID, dataRoot string, initial *droidArchivePosition, ready <-chan struct{}) *droidChildTail {
	clock := a.clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	tail := &droidChildTail{
		agent: a, sessionID: sessionID, dataRoot: dataRoot,
		workDir: a.workingDir, clock: clock, ready: ready,
		done: make(chan struct{}), stopped: make(chan struct{}),
	}
	if initial != nil && initial.headerSeen {
		tail.offset = initial.offset
		tail.headerSeen = true
		tail.fileInfo = initial.fileInfo
	}
	return tail
}

// probeChildArchiveEnd verifies the child archive header through a root
// confined to the Factory data directory and returns the current EOF offset.
// A missing directory or file returns (nil, nil) so a brand-new archive starts
// cleanly at offset 0.
func (a *Agent) probeChildArchiveEnd(sessionID string) (pos *droidArchivePosition, err error) {
	if !filepath.IsAbs(a.workingDir) || !validDroidSessionID(sessionID) {
		return nil, errors.New("droid child archive path is not valid")
	}
	dataRoot := droidDataRoot(agent.StoredSessionQuery{HomeDir: a.homeDir})
	if dataRoot == "" {
		return nil, errors.New("droid data root is unavailable")
	}
	root, err := os.OpenRoot(dataRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	project := filepath.Join(droidSessionsDirName, droidSanitizeCwd(a.workingDir))
	for _, dir := range []string{droidSessionsDirName, project} {
		info, statErr := root.Lstat(dir)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, nil
			}
			return nil, statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("droid child archive directory is not a regular directory: %s", dir)
		}
	}
	fileName := filepath.Join(project, sessionID+droidSessionSuffix)
	pathInfo, err := root.Lstat(fileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
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
	if !opened.Mode().IsRegular() || !os.SameFile(pathInfo, opened) {
		return nil, errors.New("droid child archive file changed identity")
	}
	if opened.Size() == 0 {
		return nil, nil
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, droidArchiveLineLimit+1), 4096)
	firstLine, readErr := reader.ReadBytes('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if !bytes.HasSuffix(firstLine, []byte{'\n'}) {
		if len(firstLine) > droidArchiveLineLimit {
			return nil, errors.New("droid child archive header exceeds limit")
		}
		return nil, nil
	}
	var record droidArchiveRecord
	if err := json.Unmarshal(bytes.TrimSpace(firstLine), &record); err != nil {
		return nil, fmt.Errorf("droid child archive header unreadable: %w", err)
	}
	if record.Type != droidSessionStartType || record.ID != sessionID || !sessionstore.SameDir(record.Cwd, a.workingDir) {
		return nil, errors.New("droid child archive identity mismatch")
	}
	return &droidArchivePosition{
		offset:     opened.Size(),
		headerSeen: true,
		fileInfo:   opened,
	}, nil
}

// startChildTail starts one reader for a native child that the registry linked.
func (a *Agent) startChildTail(sessionID string) {
	a.ensureChildTail(sessionID, false, nil, nil)
}

// resumeChildTail follows a send that the native child process accepted and
// reports whether a healthy tail is tracking the child archive.
func (a *Agent) resumeChildTail(sessionID string, initial *droidArchivePosition, ready <-chan struct{}) bool {
	return a.ensureChildTail(sessionID, true, initial, ready)
}

// resumeChildTailFromEOF resumes tailing a child session after a Task({resume})
// announcement, starting at EOF if no previous tail exists in memory.
func (a *Agent) resumeChildTailFromEOF(sessionID string) {
	a.tailMu.Lock()
	hasTail := a.childTails[sessionID] != nil
	a.tailMu.Unlock()
	var initial *droidArchivePosition
	if !hasTail {
		pos, err := a.probeChildArchiveEnd(sessionID)
		if err == nil {
			initial = pos
		}
	}
	a.ensureChildTail(sessionID, true, initial, nil)
}

func (a *Agent) ensureChildTail(sessionID string, continuation bool, initial *droidArchivePosition, ready <-chan struct{}) bool {
	if a.beforeChildTailStart != nil {
		a.beforeChildTailStart()
	}
	if !filepath.IsAbs(a.workingDir) || !validDroidSessionID(sessionID) {
		return false
	}
	dataRoot := droidDataRoot(agent.StoredSessionQuery{HomeDir: a.homeDir})
	if dataRoot == "" {
		return false
	}
	a.tailMu.Lock()
	if a.tailClosing {
		a.tailMu.Unlock()
		return false
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
		if old.invalid {
			old.mu.Unlock()
			a.tailMu.Unlock()
			return false
		}
		if !old.finished {
			if continuation {
				old.pendingTurns++
			}
			old.mu.Unlock()
			a.tailMu.Unlock()
			return true
		}
	}
	tail := newDroidChildTail(a, sessionID, dataRoot, initial, ready)
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
	return true
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
		if t.ready != nil {
			select {
			case <-t.ready:
			case <-t.done:
				return
			case <-t.agent.Context().Done():
				return
			}
		}
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
		if t.isInvalid() {
			t.closeInvalidChild()
		}
	}()
}

func (t *droidChildTail) stop() {
	t.stopOnce.Do(func() { close(t.done) })
	<-t.stopped
}

// finish marks the tail finished when no continuation turn is pending and stops
// its poll loop so settled children do not leak a 250ms ticker.
func (t *droidChildTail) finish() {
	t.mu.Lock()
	if t.pendingTurns > 0 {
		t.mu.Unlock()
		return
	}
	t.finished = true
	t.mu.Unlock()
	t.stop()
}

func (t *droidChildTail) isDone() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finished || t.invalid
}

func (t *droidChildTail) isInvalid() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.invalid
}

// closeInvalidChild closes any still-open registry row when the archive reader
// aborts on an invalid file. Runs outside t.mu to preserve lock order.
func (t *droidChildTail) closeInvalidChild() {
	t.agent.dispatchMu.Lock()
	defer t.agent.dispatchMu.Unlock()
	_, status, exists, err := t.agent.sink.LookupBackgroundTask(t.sessionID)
	if err != nil || !exists || status.IsFinished() {
		return
	}
	t.agent.closeChildSession(t.sessionID, bgtask.StatusEndedWithUnknownOutcome)
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
		// An archive block is committed. Store it before the next block in this message.
		t.agent.flushPendingToolCall(target, id)
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
