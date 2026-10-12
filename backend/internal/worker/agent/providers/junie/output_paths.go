package junie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

var junieTaskIDPattern = regexp.MustCompile(contracts.JunieOutputReferenceTaskIDPattern)
var junieOutputLeafPattern = regexp.MustCompile(contracts.JunieOutputReferenceFileNamePattern)

type junieOutputFilePath struct {
	SessionID        string `json:"sessionId"`
	CallID           string `json:"toolCallId"`
	TaskID           string `json:"taskId"`
	Command          string `json:"command"`
	WorkingDirectory string `json:"cwd"`
	Path             string `json:"path"`
	ExitCode         *int   `json:"exitCode"`
}

type junieTerminalExit struct {
	CallID   string  `json:"terminal_id"`
	ExitCode *int    `json:"exit_code"`
	Signal   *string `json:"signal"`
}

type junieExecuteFrame struct {
	SessionUpdate string `json:"sessionUpdate"`
	CallID        string `json:"toolCallId"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Input         struct {
		Command string `json:"command"`
		CWD     string `json:"cwd"`
	} `json:"rawInput"`
	Output struct {
		Text     *string         `json:"output"`
		ExitCode json.RawMessage `json:"exitCode"`
	} `json:"rawOutput"`
	Meta map[string]json.RawMessage `json:"_meta"`
}

func junieCompletedExecute(original []byte) (junieExecuteFrame, bool) {
	var frame junieExecuteFrame
	if json.Unmarshal(original, &frame) != nil || frame.SessionUpdate != contracts.ACPUpdateToolCallUpdate || frame.Kind != "execute" || frame.Status != "completed" ||
		strings.TrimSpace(frame.Input.Command) == "" || !filepath.IsAbs(frame.Input.CWD) || frame.Output.Text == nil {
		return frame, false
	}
	id, err := uuid.Parse(frame.CallID)
	if err != nil || id.String() != frame.CallID {
		return frame, false
	}
	var exit junieTerminalExit
	if json.Unmarshal(frame.Meta[contracts.JunieTerminalMetaExit], &exit) != nil || exit.CallID != frame.CallID || exit.ExitCode == nil || *exit.ExitCode != 0 || exit.Signal != nil {
		return frame, false
	}
	if len(frame.Output.ExitCode) > 0 {
		var code *int
		if json.Unmarshal(frame.Output.ExitCode, &code) != nil || code == nil || *code != *exit.ExitCode {
			return frame, false
		}
	}
	return frame, true
}

func junieOutputPathParts(home, path, sessionID, taskID string) ([]string, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) ||
		!safeJunieSessionID(sessionID) || !junieTaskIDPattern.MatchString(taskID) {
		return nil, errors.New("the Junie output path has an invalid native owner or path")
	}
	relative, err := filepath.Rel(home, path)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 5 || parts[0] != "sessions" || parts[1] != sessionID || parts[2] != taskID || parts[3] != "terminal-output" || !junieOutputLeafPattern.MatchString(parts[4]) {
		return nil, errors.New("the Junie output path belongs to another native session or task")
	}
	return parts, nil
}

type junieTerminalEvent struct {
	Kind   string `json:"kind"`
	TaskID string `json:"taskId"`
	Event  struct {
		Terminal struct {
			Kind     string  `json:"kind"`
			StepID   string  `json:"stepId"`
			Status   string  `json:"status"`
			Command  string  `json:"command"`
			Output   *string `json:"output"`
			ExitCode *int    `json:"exitCode"`
			Agent    struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"agent"`
			File struct {
				Path string `json:"relativePath"`
			} `json:"outputFile"`
		} `json:"agentEvent"`
	} `json:"event"`
}

type junieCompletedTerminalOwner struct {
	taskID, callID, command, path, output string
	agentKind, agentID                    string
	exitCode                              int
}

func junieTerminalOutputEvent(data []byte, frame junieExecuteFrame) (*junieTerminalEvent, error) {
	var selected *junieTerminalEvent
	var selectedOwner junieCompletedTerminalOwner
	lines := bytes.Split(data, []byte{'\n'})
	// The native event writer appends JSONL. A final line without a newline is incomplete.
	for _, line := range lines[:len(lines)-1] {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var header struct {
			Kind  string `json:"kind"`
			Event struct {
				AgentEvent json.RawMessage `json:"agentEvent"`
			} `json:"event"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return nil, fmt.Errorf("read the Junie output path event: %w", err)
		}
		if header.Kind != "SessionA2uxEvent" || len(header.Event.AgentEvent) == 0 || bytes.Equal(bytes.TrimSpace(header.Event.AgentEvent), []byte("null")) {
			continue
		}
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(header.Event.AgentEvent, &kind); err != nil {
			return nil, fmt.Errorf("read the Junie native event kind: %w", err)
		}
		if kind.Kind != "TerminalBlockUpdatedEvent" {
			continue
		}
		var event junieTerminalEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("read the Junie terminal event: %w", err)
		}
		terminal := event.Event.Terminal
		if event.Kind != "SessionA2uxEvent" || terminal.Kind != "TerminalBlockUpdatedEvent" || terminal.StepID != frame.CallID || terminal.Status != "COMPLETED" {
			continue
		}
		if terminal.Agent.Kind != "MainAgent" || terminal.Agent.ID != "main" || terminal.Command != frame.Input.Command || terminal.Output == nil ||
			*terminal.Output != *frame.Output.Text || terminal.ExitCode == nil || *terminal.ExitCode != 0 || terminal.File.Path == "" {
			return nil, errors.New("the Junie output path event differs from its native call")
		}
		owner := junieCompletedTerminalOwner{
			taskID: event.TaskID, callID: terminal.StepID, command: terminal.Command, path: terminal.File.Path, output: *terminal.Output,
			agentKind: terminal.Agent.Kind, agentID: terminal.Agent.ID, exitCode: *terminal.ExitCode,
		}
		if selected != nil {
			if owner != selectedOwner {
				return nil, errors.New("the Junie output path has duplicate completed native events with different owners")
			}
			// Junie repeats the completed terminal snapshot when the enclosing task ends.
			// The task state and timestamp do not change that terminal's native owner.
			continue
		}
		selected = &event
		selectedOwner = owner
	}
	return selected, nil
}

// readJunieOutputPathFromRoot reads native session metadata under one confined archive root.
// It validates the reported path without opening the output log.
func readJunieOutputPathFromRoot(ctx context.Context, root sessionstore.ArchiveRoot, home, workingDir, sessionID string, original []byte, maximum int) ([]byte, error) {
	frame, ok := junieCompletedExecute(original)
	if !ok {
		return nil, nil
	}
	if !safeJunieSessionID(sessionID) || frame.Input.CWD != workingDir {
		return nil, errors.New("the Junie output path has another session or working directory")
	}
	if maximum <= 0 {
		return nil, errors.New("the Junie output path requires a positive message size limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	summaryBytes, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, junieSummaryReadLimit, "sessions", sessionID, "summary.json")
	if err != nil {
		return nil, err
	}
	var summary junieStoredSummary
	if json.Unmarshal(summaryBytes, &summary) != nil || summary.SessionID != sessionID || summary.ProjectDir != workingDir {
		return nil, errors.New("the Junie output path summary belongs to another session or project")
	}
	events, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, junieEventsReadLimit, "sessions", sessionID, "events.jsonl")
	if err != nil {
		return nil, err
	}
	event, err := junieTerminalOutputEvent(events, frame)
	if err != nil || event == nil {
		return nil, err
	}
	_, err = junieOutputPathParts(home, event.Event.Terminal.File.Path, sessionID, event.TaskID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var originalFields map[string]json.RawMessage
	if err := json.Unmarshal(original, &originalFields); err != nil {
		return nil, err
	}
	supplement := acp.NewToolSupplement(originalFields)
	exitCode := 0
	receipt := junieOutputFilePath{SessionID: sessionID, CallID: frame.CallID, TaskID: event.TaskID, Command: frame.Input.Command, WorkingDirectory: workingDir,
		Path: event.Event.Terminal.File.Path, ExitCode: &exitCode}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	supplement[contracts.JunieSupplementOutputFilePath] = encoded
	extra, err := json.Marshal(supplement)
	if err != nil {
		return nil, err
	}
	if len(extra) > maximum {
		return nil, errors.New("the Junie output path receipt exceeds the message size limit")
	}
	return extra, nil
}

func readJunieOutputPath(ctx context.Context, home, workingDir, sessionID string, original []byte) (data []byte, err error) {
	root, err := sessionstore.OpenArchiveRoot(home)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			data, err = nil, errors.Join(err, closeErr)
		}
	}()
	return readJunieOutputPathFromRoot(ctx, root, home, workingDir, sessionID, original, agent.LiveMaxMessageSize()-len(original)-1024)
}
