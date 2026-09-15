package agent

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Identify direct task calls and tasks inside Reasonix's capability wrapper.
func reasonixSubagentFromToolCall(tc acpToolCallEnvelope) *acpSubagentObservation {
	toolName, rawInput := resolveReasonixTask(tc.Title, tc.RawInput)
	if toolName == "" {
		return nil
	}
	var input struct {
		Description string `json:"description"`
	}
	// Invalid or incomplete arguments keep the same launch layout.
	title := ""
	if json.Unmarshal(rawInput, &input) == nil {
		title = strings.TrimSpace(input.Description)
	}
	if title == "" {
		title = "Reasonix subagent"
	}
	// This registry path does not create a child transcript, so it retains no prompt copy.
	return &acpSubagentObservation{
		RowKey: tc.ToolCallID,
		Title:  title,
		Status: bgtask.StatusRunning,
		Spawns: true,
	}
}

// Resolve capability arguments without changing the provider's original message.
func resolveReasonixTask(title string, rawInput json.RawMessage) (string, json.RawMessage) {
	if title == contracts.ReasonixToolUseCapability {
		var capability struct {
			Action    string          `json:"action"`
			ID        string          `json:"capability_id"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(rawInput, &capability) != nil || capability.Action != contracts.ReasonixCapabilityActionCall ||
			!strings.HasPrefix(capability.ID, contracts.ReasonixCapabilityPrefixTool) {
			return "", nil
		}
		title = strings.TrimPrefix(capability.ID, contracts.ReasonixCapabilityPrefixTool)
		rawInput = capability.Arguments
	}
	if title != contracts.ReasonixToolTask && title != contracts.ReasonixToolReadOnlyTask {
		return "", nil
	}
	return title, rawInput
}

var reasonixOutcomeHeader = regexp.MustCompile(`^(?:Subagent reference(?: \(failed\))?: sa_[\w-]*\n)?Subagent outcome: status=(completed|partial|failed|cancelled) retryable=(?:true|false)(?: error_code=\S+)?(?:\n|$)`)

// A completed background launch acknowledges dispatch, not the subagent's completion.
func reasonixSubagentFromToolCallUpdate(tcu acpToolCallUpdateEnvelope) *acpSubagentObservation {
	if !acpStatusIsFinal(tcu.Status) {
		return nil
	}
	status := acpFinalStatus(tcu.Status)
	toolName, rawInput := resolveReasonixTask(tcu.Title, tcu.RawInput)
	// A successful read-only task returns plain report text, not a status envelope.
	if tcu.Status != "cancelled" && (tcu.Status != "completed" || toolName != contracts.ReasonixToolReadOnlyTask) {
		outcome := reasonixOutcomeHeader.FindStringSubmatch(acpToolCallText(tcu.Content))
		if len(outcome) > 1 {
			switch outcome[1] {
			case "failed":
				status = bgtask.StatusFailed
			case "cancelled":
				status = bgtask.StatusStopped
			}
		} else if tcu.Status == "completed" {
			var args struct {
				Background bool `json:"run_in_background"`
			}
			_ = json.Unmarshal(rawInput, &args)
			if args.Background || strings.HasPrefix(acpToolCallText(tcu.Content), "Started background task ") {
				return nil
			}
		}
	}
	return &acpSubagentObservation{RowKey: tcu.ToolCallID, Status: status, CloseRow: true, Mode: acpModeCloseOnly}
}
