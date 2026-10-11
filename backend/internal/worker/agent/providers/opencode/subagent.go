package opencode

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// IsHiddenPrimaryAgent reports whether a primary-agent id is an internal
// pseudo-agent that must be hidden from the picker. These ids originate in
// OpenCode's protocol but are shared by every OpenCode-family ACP provider
// (Kilo included), so both inject this as their primaryAgentHiddenFilter.
func IsHiddenPrimaryAgent(id string) bool {
	switch id {
	case HiddenCompaction, openCodeHiddenTitle, openCodeHiddenSummary:
		return true
	default:
		return false
	}
}

// OpenCode and Kilo identify the native task before its arguments arrive.
// Other titles require the native prompt and subagent discriminator.
func SubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	return openCodeSpawnObservation(tc.ToolCallID, tc.Title, tc.RawInput, tc.Title == "task" && tc.Kind == "think")
}

// Build the registry row from a known native task or its later arguments.
// Both event paths use the tool-call ID, so later arguments update the same row.
func openCodeSpawnObservation(toolCallID, callTitle string, rawInput json.RawMessage, knownTask bool) *acp.SubagentObservation {
	var input struct {
		Description  string          `json:"description"`
		Prompt       json.RawMessage `json:"prompt"`
		SubagentType string          `json:"subagent_type"`
		TaskID       json.RawMessage `json:"task_id"`
		// Some native builds use subagentID instead of subagent_type.
		// Either discriminator requires a parsed prompt.
		SubagentID string `json:"subagentID"`
	}
	inputParsed := json.Unmarshal(rawInput, &input) == nil
	prompt, promptParsed := openCodeTaskString(input.Prompt)
	taskID, taskIDParsed := "", true
	if len(input.TaskID) > 0 {
		taskID, taskIDParsed = openCodeTaskString(input.TaskID)
	}
	argumentsReady := inputParsed && promptParsed && taskIDParsed && (input.SubagentType != "" || input.SubagentID != "")
	// An unidentified tool requires parsed task arguments.
	if !knownTask && !argumentsReady {
		return nil
	}
	title := callTitle
	if title == "" || title == "task" {
		title = input.Description
	}
	if title == "" {
		title = input.SubagentType
	}
	if title == "" {
		title = "Subagent"
	}
	spawnSpanID := ""
	if argumentsReady && taskID == "" {
		spawnSpanID = toolCallID
	}
	return &acp.SubagentObservation{
		RowKey: toolCallID, Title: title, Status: bgtask.StatusRunning,
		ChildSpawnSpanID: spawnSpanID, Prompt: prompt, Spawns: true,
	}
}

// openCodeTaskString rejects absent, null, and non-string arguments. Empty strings remain valid.
func openCodeTaskString(raw json.RawMessage) (string, bool) {
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return "", false
	}
	return *value, true
}

// SubagentFromToolCallUpdate closes a final row and adopts the native child key from valid task metadata.
// RenameFrom preserves the initial registry row. ChildSpawnSpanID preserves the native invocation identity.
func SubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		// Kilo first supplies task arguments in an in-progress update.
		// Its initial tool_call can contain rawInput: {}.
		// Detect that request here so the final update closes an existing registry row.
		//
		// A non-final spawn update after completion recreates the tool-call row.
		// session/load replay can then send the final update again.
		// That update renames the recreated row to the existing native session row.
		// RenameBackgroundTask drops the duplicate source row on this collision.
		// The replay therefore retains one row and leaves no extra running row.
		return openCodeSpawnObservation(tcu.ToolCallID, tcu.Title, tcu.RawInput, false)
	}
	// The final rawOutput may carry the child session id under metadata.
	rowKey := tcu.ToolCallID
	renameFrom := ""
	childKey := ""
	spawnSpanID := ""
	background := false
	if len(tcu.RawOutput) > 0 {
		var out struct {
			Metadata struct {
				SessionID  string `json:"sessionId"`
				Background bool   `json:"background"`
			} `json:"metadata"`
		}
		if json.Unmarshal(tcu.RawOutput, &out) == nil {
			background = out.Metadata.Background
			if out.Metadata.SessionID != "" {
				rowKey = out.Metadata.SessionID
				renameFrom = tcu.ToolCallID
				childKey = out.Metadata.SessionID
				spawnSpanID = tcu.ToolCallID
			}
		}
	}
	return &acp.SubagentObservation{
		RowKey:           rowKey,
		RenameFrom:       renameFrom,
		ChildAgentKey:    childKey,
		ChildSpawnSpanID: spawnSpanID,
		Title:            tcu.Title,
		Status:           acp.FinalStatus(tcu.Status),
		CloseRow:         true,
		Mode:             acp.ModeCloseOnly,
		ReportID:         tcu.ToolCallID,
		Report: agent.SubagentReport{
			Text: openCodeSubagentReport(acp.ToolCallText(tcu.Content), background),
		},
	}
}

func openCodeSubagentReport(text string, background bool) string {
	if background {
		return ""
	}
	text = strings.TrimSpace(text)
	const start = "<task_result>"
	const end = "</task_result>"
	if _, after, ok := strings.Cut(text, start); ok {
		if report, _, found := strings.Cut(after, end); found {
			return strings.TrimSpace(report)
		}
	}
	return text
}
