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
		// Some builds spell it with a different key; the shape still carries a
		// prompt + a type, so detect on the union.
		SubagentID string `json:"subagentID"`
	}
	if len(rawInput) == 0 && !knownTask {
		return nil
	}
	if err := json.Unmarshal(rawInput, &input); err != nil && !knownTask {
		return nil
	}
	// An unidentified tool requires a prompt and a subagent discriminator.
	if !knownTask && (len(input.Prompt) == 0 || (input.SubagentType == "" && input.SubagentID == "")) {
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
	prompt := ""
	_ = json.Unmarshal(input.Prompt, &prompt)
	return &acp.SubagentObservation{
		RowKey:        toolCallID,
		Title:         title,
		Status:        bgtask.StatusRunning,
		ChildAgentKey: toolCallID,
		Prompt:        prompt,
		Spawns:        true,
	}
}

// SubagentFromToolCallUpdate closes the registry row on a final
// status, and when rawOutput.metadata.sessionId is present, re-keys the row to
// the child session id (the metadata surfaces only on the final update).
// The spawn row was opened under the toolCallId, so SpawnRowKey carries it to
// keep the close from leaking it as a Running row.
func SubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		// Not final: this is where Kilo first reveals the spawn shape (its
		// tool_call carries `rawInput: {}`), so run the same detection here.
		// Without it a Kilo spawn produced no registry row at all -- the
		// final update below then closed a row that was never opened.
		//
		// A spawn-shaped update that arrives AFTER the final one re-creates the
		// row under the toolCallId, because the final update already renamed the
		// original to the child session id. A `session/load` history replay then
		// redelivers the final update, whose rename collides with the surviving
		// session-id row. RenameBackgroundTask resolves that collision by dropping
		// the re-created duplicate, so the replay converges on one row instead of
		// leaving a Running row that no later event closes.
		return openCodeSpawnObservation(tcu.ToolCallID, tcu.Title, tcu.RawInput, false)
	}
	// The final rawOutput may carry the child session id under metadata.
	rowKey := tcu.ToolCallID
	renameFrom := ""
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
		}
		if out.Metadata.SessionID != "" {
			// Rename the spawn row (toolCallId) to the child session id so one
			// row tracks the lifecycle, then give a final status to it.
			rowKey = out.Metadata.SessionID
			renameFrom = tcu.ToolCallID
		}
	}
	return &acp.SubagentObservation{
		RowKey:        rowKey,
		RenameFrom:    renameFrom,
		ChildAgentKey: rowKey,
		Status:        acp.FinalStatus(tcu.Status),
		CloseRow:      true,
		Mode:          acp.ModeCloseOnly,
		ReportID:      tcu.ToolCallID,
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
