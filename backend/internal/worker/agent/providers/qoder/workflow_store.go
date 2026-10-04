package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	qoderWorkflowOutputLimit  = 1 << 20
	qoderWorkflowJournalLimit = 4 << 20
	qoderWorkflowChildLimit   = 16 << 20
)

var errQoderArchiveIncomplete = errors.New("the Qoder workflow archive is incomplete")

func qoderIncompleteArchive(err error) error {
	return fmt.Errorf("%w: %w", errQoderArchiveIncomplete, err)
}

type qoderWorkflowArchive struct {
	name     string
	children map[string]qoderArchivedChild
	order    []string
}

type qoderArchivedChild struct {
	prompt   string
	status   bgtask.Status
	messages []qoderArchivedMessage
}

type qoderArchivedMessage struct {
	initialPrompt bool
	userText      string
	raw           json.RawMessage
}

// Qoder streams workflow lifecycle events but keeps each child's messages in
// its native archive. The journal links a child task ID to that archive.
func readQoderWorkflowArchive(opts agent.Options, sessionID string, event *qoderTaskEvent, run *qoderWorkflowRun) (qoderWorkflowArchive, error) {
	return readQoderWorkflowArchiveWithOpener(opts, sessionID, event, run, openQoderArchiveRoot)
}

func readQoderWorkflowArchiveWithOpener(opts agent.Options, sessionID string, event *qoderTaskEvent, run *qoderWorkflowRun,
	opener func(string) (qoderArchiveReadRoot, error),
) (archive qoderWorkflowArchive, err error) {
	if opts.WorkingDir == "" || !filepath.IsAbs(opts.WorkingDir) || !qoderSafeFileID(sessionID) || event.OutputFile == "" {
		return qoderWorkflowArchive{}, errors.New("the Qoder workflow archive identity is incomplete")
	}
	workflowRoot := filepath.Join(opts.WorkingDir, ".qoder", "sessions", sessionID, "workflows", "runs")
	relative, err := filepath.Rel(workflowRoot, filepath.Clean(event.OutputFile))
	if err != nil {
		return qoderWorkflowArchive{}, fmt.Errorf("check the Qoder workflow output path: %w", err)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 2 || !qoderSafeFileID(parts[0]) || parts[1] != "output.json" || (run.runID != "" && run.runID != parts[0]) {
		return qoderWorkflowArchive{}, errors.New("the Qoder workflow output path does not match the run")
	}
	runID := parts[0]
	workingRoot, err := opener(opts.WorkingDir)
	if err != nil {
		return qoderWorkflowArchive{}, fmt.Errorf("open the Qoder working directory: %w", err)
	}
	defer func() {
		if closeErr := workingRoot.Close(); err == nil && closeErr != nil {
			archive, err = qoderWorkflowArchive{}, closeErr
		}
	}()
	outputBytes, err := qoderReadRegularFile(workingRoot, qoderWorkflowOutputLimit, ".qoder", "sessions", sessionID, "workflows", "runs", runID, "output.json")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return qoderWorkflowArchive{}, qoderIncompleteArchive(err)
		}
		return qoderWorkflowArchive{}, fmt.Errorf("read the Qoder workflow output: %w", err)
	}
	var output struct {
		RunID        string `json:"runId"`
		TaskID       string `json:"taskId"`
		WorkflowName string `json:"workflowName"`
		AgentCount   int    `json:"agentCount"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(outputBytes, &output); err != nil {
		return qoderWorkflowArchive{}, qoderIncompleteArchive(fmt.Errorf("decode the Qoder workflow output: %w", err))
	}
	if output.RunID != runID || output.TaskID != event.TaskID || output.AgentCount < 0 {
		return qoderWorkflowArchive{}, errors.New("the Qoder workflow output has a different run identity")
	}
	if event.requireFinalStatus {
		if _, known := qoderFinalChildStatus(output.Status); !known || output.Status != event.Status {
			return qoderWorkflowArchive{}, errors.New("the Qoder replay notification does not match the canonical final status")
		}
	}
	name := bgtask.FirstLine(output.WorkflowName)
	if name == "" {
		name = run.label
	}
	journal, err := qoderReadRegularFile(workingRoot, qoderWorkflowJournalLimit, ".qoder", "sessions", sessionID, "workflows", "runs", runID, "journal.jsonl")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Native zero-child workflows never append a journal.
			if output.AgentCount == 0 && len(run.children) == 0 && len(run.order) == 0 {
				return qoderWorkflowArchive{name: name, children: map[string]qoderArchivedChild{}}, nil
			}
			return qoderWorkflowArchive{}, qoderIncompleteArchive(err)
		}
		return qoderWorkflowArchive{}, fmt.Errorf("read the Qoder workflow journal: %w", err)
	}
	archive = qoderWorkflowArchive{name: name, children: make(map[string]qoderArchivedChild)}
	configDir := qoderConfigRoot(opts)
	if !filepath.IsAbs(configDir) {
		configDir = filepath.Join(opts.WorkingDir, configDir)
	}
	configRoot, err := opener(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return qoderWorkflowArchive{}, qoderIncompleteArchive(err)
		}
		return qoderWorkflowArchive{}, fmt.Errorf("open the Qoder configuration directory: %w", err)
	}
	defer func() {
		if closeErr := configRoot.Close(); err == nil && closeErr != nil {
			archive, err = qoderWorkflowArchive{}, closeErr
		}
	}()
	project := qoderProjectSlug(opts.WorkingDir)
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	scanner.Buffer(make([]byte, 0, 64*1024), qoderWorkflowJournalLimit)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			AgentID string `json:"agentId"`
			Result  struct {
				State          string `json:"state"`
				OutputPath     string `json:"outputPath"`
				TranscriptPath string `json:"transcriptPath"`
			} `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return qoderWorkflowArchive{}, qoderIncompleteArchive(fmt.Errorf("decode a Qoder workflow journal row: %w", err))
		}
		if entry.Type != "result" {
			continue
		}
		if !qoderSafeFileID(entry.AgentID) || !strings.HasSuffix(entry.Result.OutputPath, ".output") {
			return qoderWorkflowArchive{}, errors.New("a Qoder workflow child has no usable identity")
		}
		taskID := strings.TrimSuffix(filepath.Base(entry.Result.OutputPath), ".output")
		if !qoderSafeFileID(taskID) {
			return qoderWorkflowArchive{}, errors.New("a Qoder workflow child task ID is invalid")
		}
		if _, exists := archive.children[taskID]; exists {
			return qoderWorkflowArchive{}, errors.New("the Qoder workflow journal repeats a child task ID")
		}
		childStatus, ok := qoderArchiveChildStatus(entry.Result.State)
		if !ok {
			return qoderWorkflowArchive{}, fmt.Errorf("the Qoder workflow child state %q is unknown", entry.Result.State)
		}
		childFile := "agent-" + entry.AgentID + ".jsonl"
		expected := filepath.Join(configDir, qoderProjectsDirName, project, sessionID, "subagents", childFile)
		if filepath.Clean(entry.Result.TranscriptPath) != expected {
			return qoderWorkflowArchive{}, errors.New("a Qoder workflow child transcript path differs from its session")
		}
		history, err := qoderReadRegularFile(configRoot, qoderWorkflowChildLimit, qoderProjectsDirName, project, sessionID, "subagents", childFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return qoderWorkflowArchive{}, qoderIncompleteArchive(err)
			}
			return qoderWorkflowArchive{}, fmt.Errorf("read a Qoder workflow child transcript: %w", err)
		}
		messages, prompt, err := qoderArchiveMessages(history, sessionID, event.ToolUseID, entry.AgentID, opts.WorkingDir, childStatus)
		if err != nil {
			return qoderWorkflowArchive{}, err
		}
		if started := run.children[taskID]; started != nil && started.prompt != prompt {
			return qoderWorkflowArchive{}, errors.New("a Qoder workflow child prompt differs from its task start")
		}
		archive.children[taskID] = qoderArchivedChild{prompt: prompt, status: childStatus, messages: messages}
		archive.order = append(archive.order, taskID)
	}
	if err := scanner.Err(); err != nil {
		return qoderWorkflowArchive{}, fmt.Errorf("scan the Qoder workflow journal: %w", err)
	}
	if len(archive.children) != output.AgentCount {
		return qoderWorkflowArchive{}, qoderIncompleteArchive(errors.New("the Qoder workflow journal has a different child count"))
	}
	var missing []string
	for taskID := range run.children {
		if _, found := archive.children[taskID]; !found {
			missing = append(missing, taskID)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return qoderWorkflowArchive{}, qoderIncompleteArchive(fmt.Errorf("the Qoder workflow journal omits started children: %s", strings.Join(missing, ", ")))
	}
	return archive, nil
}

func qoderArchiveChildStatus(state string) (bgtask.Status, bool) {
	switch state {
	case "done":
		return bgtask.StatusCompleted, true
	case "error":
		return bgtask.StatusFailed, true
	case "stopped", "cancelled":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusUnspecified, false
	}
}

func qoderArchiveMessages(history []byte, sessionID, toolID, agentID, workingDir string, status bgtask.Status) ([]qoderArchivedMessage, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(history))
	scanner.Buffer(make([]byte, 0, 64*1024), qoderWorkflowChildLimit)
	var messages []qoderArchivedMessage
	prompt := ""
	answer := false
	for scanner.Scan() {
		var record struct {
			Type            string `json:"type"`
			SessionID       string `json:"sessionId"`
			ParentToolUseID string `json:"parent_tool_use_id"`
			AgentID         string `json:"agentId"`
			Cwd             string `json:"cwd"`
			Message         struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, "", qoderIncompleteArchive(fmt.Errorf("decode a Qoder workflow child row: %w", err))
		}
		if record.Type != "user" && record.Type != "assistant" {
			continue
		}
		if len(messages) == 0 && record.Type != "user" {
			return nil, "", qoderIncompleteArchive(errors.New("the Qoder workflow child transcript must start with its prompt"))
		}
		if record.Type != record.Message.Role || record.SessionID != sessionID || record.ParentToolUseID != toolID || record.AgentID != agentID || record.Cwd != workingDir {
			return nil, "", errors.New("a Qoder workflow child row belongs to a different task")
		}
		message := qoderArchivedMessage{raw: append(json.RawMessage(nil), scanner.Bytes()...)}
		if record.Type == "user" {
			text := qoderArchiveUserText(record.Message.Content)
			if prompt == "" {
				if text == "" {
					return nil, "", errors.New("the Qoder workflow child has no text prompt")
				}
				prompt = text
				message.initialPrompt = true
			} else {
				message.userText = text
			}
		} else {
			answer = true
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("scan a Qoder workflow child transcript: %w", err)
	}
	if prompt == "" || (status == bgtask.StatusCompleted && !answer) {
		return nil, "", qoderIncompleteArchive(errors.New("the Qoder workflow child transcript lacks a prompt or answer"))
	}
	return messages, prompt, nil
}

func qoderArchiveUserText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func qoderSafeFileID(value string) bool {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && ch != '-' && ch != '_' {
			return false
		}
	}
	return true
}

type qoderArchiveReadRoot = sessionstore.ArchiveRoot

func openQoderArchiveRoot(path string) (qoderArchiveReadRoot, error) {
	// OpenRoot follows a configured-root symlink and confines descendant reads
	// to the directory it actually opened.
	return sessionstore.OpenArchiveRoot(path)
}

// qoderReadRegularFile checks each archive component and the opened file.
func qoderReadRegularFile(root qoderArchiveReadRoot, limit int64, parts ...string) ([]byte, error) {
	data, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, limit, parts...)
	if err != nil {
		if errors.Is(err, sessionstore.ErrArchiveFileModeOrSize) {
			return nil, fmt.Errorf("the Qoder archive file is not regular within %d bytes: %w", limit, err)
		}
		return nil, fmt.Errorf("read the Qoder archive file: %w", err)
	}
	return data, nil
}
