package codebuddy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const (
	codebuddyWorkflowJournalLimit = 4 << 20
	codebuddyChildHistoryLimit    = 16 << 20
)

var errCodebuddyArchiveIncomplete = errors.New("the CodeBuddy workflow archive is incomplete")

func codebuddyIncompleteArchive(err error) error {
	return fmt.Errorf("%w: %w", errCodebuddyArchiveIncomplete, err)
}

// codebuddyArchiveRecord keeps the native JSONL row for browser extraction.
type codebuddyArchiveRecord struct {
	Type        string          `json:"type"`
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	CallID      string          `json:"callId"`
	SnakeCallID string          `json:"call_id"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Raw         json.RawMessage `json:"-"`
}

func (r codebuddyArchiveRecord) toolCallID() string {
	if r.CallID != "" {
		return r.CallID
	}
	return r.SnakeCallID
}

// readCodebuddyWorkflowChild finds a child through the workflow journal's
// exact key-to-session link. The child file and every path component must stay
// inside the selected CodeBuddy session without following a symlink.
func readCodebuddyWorkflowChild(configDir, workingDir, parentSessionID, runID, childKey string) ([]codebuddyArchiveRecord, error) {
	return readCodebuddyWorkflowChildWithOpener(configDir, workingDir, parentSessionID, runID, childKey, openCodebuddyArchiveRoot)
}

func readCodebuddyWorkflowChildWithOpener(configDir, workingDir, parentSessionID, runID, childKey string,
	opener func(string) (codebuddyArchiveReadRoot, error),
) (records []codebuddyArchiveRecord, err error) {
	if configDir == "" || workingDir == "" || !safeCodebuddyFileID(parentSessionID) || !safeCodebuddyFileID(runID) || childKey == "" {
		return nil, errors.New("the CodeBuddy workflow child identity is incomplete")
	}
	root, err := opener(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, codebuddyIncompleteArchive(err)
		}
		return nil, fmt.Errorf("open the CodeBuddy configuration directory: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); err == nil && closeErr != nil {
			records, err = nil, closeErr
		}
	}()
	project := codebuddyProjectSlug(workingDir)
	journal, err := codebuddyReadRegularFile(root, codebuddyWorkflowJournalLimit,
		codebuddyProjectsDirName, project, parentSessionID, "subagents", "workflows", "wf_"+runID, "journal.jsonl")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, codebuddyIncompleteArchive(err)
		}
		return nil, fmt.Errorf("read the CodeBuddy workflow journal: %w", err)
	}
	childSessionID, err := codebuddyJournalChildSession(journal, runID, childKey)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(childSessionID, "agent-") || !safeCodebuddyFileID(childSessionID) {
		return nil, errors.New("the CodeBuddy workflow child session ID is invalid")
	}
	history, err := codebuddyReadRegularFile(root, codebuddyChildHistoryLimit,
		codebuddyProjectsDirName, project, parentSessionID, "subagents", childSessionID+".jsonl")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, codebuddyIncompleteArchive(err)
		}
		return nil, fmt.Errorf("read the CodeBuddy workflow child history: %w", err)
	}
	return codebuddyArchiveRecords(history)
}

func safeCodebuddyFileID(value string) bool {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value {
		return false
	}
	for _, ch := range value {
		switch {
		case ch >= '0' && ch <= '9', ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch == '-', ch == '_':
		default:
			return false
		}
	}
	return true
}

type codebuddyArchiveReadRoot = sessionstore.ArchiveRoot

func openCodebuddyArchiveRoot(path string) (codebuddyArchiveReadRoot, error) {
	// OpenRoot follows a symlink in the configured root name, then confines
	// every relative file read to the directory it actually opened.
	return sessionstore.OpenArchiveRoot(path)
}

// codebuddyReadRegularFile checks each archive component and the opened file.
func codebuddyReadRegularFile(root codebuddyArchiveReadRoot, limit int64, parts ...string) ([]byte, error) {
	data, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, limit, parts...)
	if err != nil {
		return nil, fmt.Errorf("read the CodeBuddy child file: %w", err)
	}
	return data, nil
}

// codebuddyJournalChildSession requires one start and one finish for the same
// workflow child key. A missing or conflicting finish selects no child file.
func codebuddyJournalChildSession(journal []byte, runID, childKey string) (string, error) {
	started := 0
	finished := 0
	childSessionID := ""
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	scanner.Buffer(make([]byte, 0, 64*1024), codebuddyWorkflowJournalLimit)
	for scanner.Scan() {
		var event struct {
			Type      string `json:"type"`
			RunID     string `json:"runId"`
			Key       string `json:"key"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", codebuddyIncompleteArchive(fmt.Errorf("decode a CodeBuddy workflow journal row: %w", err))
		}
		if event.RunID != runID || event.Key != childKey {
			continue
		}
		switch event.Type {
		case "agent_started":
			started++
		case "agent_finished":
			finished++
			childSessionID = event.SessionID
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan the CodeBuddy workflow journal: %w", err)
	}
	if started == 0 || finished == 0 {
		return "", codebuddyIncompleteArchive(errors.New("the CodeBuddy workflow child journal link is missing"))
	}
	if started != 1 || finished != 1 || childSessionID == "" {
		return "", errors.New("the CodeBuddy workflow child journal link is missing or ambiguous")
	}
	return childSessionID, nil
}

func codebuddyArchiveRecords(history []byte) ([]codebuddyArchiveRecord, error) {
	var records []codebuddyArchiveRecord
	completedCalls := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(history))
	scanner.Buffer(make([]byte, 0, 64*1024), codebuddyChildHistoryLimit)
	for scanner.Scan() {
		var record codebuddyArchiveRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, codebuddyIncompleteArchive(fmt.Errorf("decode a CodeBuddy workflow child row: %w", err))
		}
		switch record.Type {
		case "message":
			if record.Role != "user" && record.Role != "assistant" {
				continue
			}
		case "function_call", "function_call_output", "function_call_result":
			if record.CallID != "" && record.SnakeCallID != "" && record.CallID != record.SnakeCallID {
				return nil, errors.New("a CodeBuddy workflow child tool record has conflicting call IDs")
			}
			callID := record.toolCallID()
			if strings.TrimSpace(callID) == "" {
				return nil, codebuddyIncompleteArchive(errors.New("a CodeBuddy workflow child tool record has no call ID"))
			}
			if record.Type == "function_call" {
				if strings.TrimSpace(record.Name) == "" {
					return nil, codebuddyIncompleteArchive(errors.New("a CodeBuddy workflow child tool call has no name"))
				}
				if _, exists := completedCalls[callID]; exists {
					return nil, fmt.Errorf("a CodeBuddy workflow child tool call reuses duplicate call ID %q", callID)
				}
				completedCalls[callID] = false
			} else {
				completed, exists := completedCalls[callID]
				if !exists {
					return nil, fmt.Errorf("a CodeBuddy workflow child tool result %q has no matching call", callID)
				}
				if completed {
					return nil, fmt.Errorf("a CodeBuddy workflow child tool call %q has a duplicate result", callID)
				}
				completedCalls[callID] = record.Status != "in_progress"
			}
		default:
			continue
		}
		record.Raw = append(json.RawMessage(nil), scanner.Bytes()...)
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan the CodeBuddy workflow child history: %w", err)
	}
	if len(records) < 2 || records[0].Role != "user" {
		return nil, codebuddyIncompleteArchive(errors.New("the CodeBuddy workflow child history has no prompt and answer"))
	}
	if strings.TrimSpace(codebuddyArchiveUserText(records[0].Content)) == "" {
		return nil, codebuddyIncompleteArchive(errors.New("the CodeBuddy workflow child history has no text prompt"))
	}
	answer := false
	for _, record := range records[1:] {
		answer = answer || record.Role == "assistant"
	}
	if !answer {
		return nil, codebuddyIncompleteArchive(errors.New("the CodeBuddy workflow child history has no assistant answer"))
	}
	for _, completed := range completedCalls {
		if !completed {
			return nil, codebuddyIncompleteArchive(errors.New("a CodeBuddy workflow child tool call has no final result"))
		}
	}
	return records, nil
}

// codebuddyArchiveUserText reads the native input_text blocks of one user row.
func codebuddyArchiveUserText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if (block.Type == "input_text" || block.Type == "text") && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
