package reasonix

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	reasonixToolFleet          = "fleet"
	reasonixToolParallelTasks  = "parallel_tasks"
	reasonixToolResearch       = "research"
	reasonixToolReview         = "review"
	reasonixToolSecurityReview = "security_review"
	reasonixToolRunSkill       = "run_skill"
	reasonixToolReadOnlySkill  = "read_only_skill"
)

// Identify direct task calls and tasks inside Reasonix's capability wrapper.
func reasonixSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	toolName, rawInput := resolveReasonixTask(tc.Title, tc.RawInput)
	if toolName == "" {
		return nil
	}
	var input struct {
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		Task         string `json:"task"`
		Query        string `json:"query"`
		Goal         string `json:"goal"`
		Instructions string `json:"instructions"`
		Skill        string `json:"skill"`
		SkillName    string `json:"skill_name"`
	}
	// Invalid or incomplete arguments keep the same launch layout.
	title := ""
	prompt := ""
	if json.Unmarshal(rawInput, &input) == nil {
		title = strings.TrimSpace(input.Description)
		switch toolName {
		case contracts.ReasonixToolTask, contracts.ReasonixToolReadOnlyTask:
			prompt = input.Prompt
		case contracts.ReasonixToolExplore:
			prompt = input.Task
		default:
			if title == "" {
				title = firstNonEmptyTrimmed(input.Skill, input.SkillName)
			}
			prompt = firstNonEmpty(input.Prompt, input.Task, input.Query, input.Goal, input.Instructions)
		}
	}
	if title == "" {
		switch toolName {
		case contracts.ReasonixToolTask, contracts.ReasonixToolReadOnlyTask, contracts.ReasonixToolExplore:
			title = "Reasonix subagent"
		default:
			title = toolName
		}
	}
	obs := &acp.SubagentObservation{
		RowKey:        tc.ToolCallID,
		Title:         title,
		Status:        bgtask.StatusRunning,
		ChildAgentKey: tc.ToolCallID,
		Prompt:        prompt,
		Spawns:        true,
	}
	if acp.StatusIsFinal(tc.Status) {
		obs.Status = acp.FinalStatus(tc.Status)
		obs.CloseRow = true
	}
	return obs
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isReasonixSubagentTool(title string) bool {
	switch title {
	case contracts.ReasonixToolTask,
		contracts.ReasonixToolReadOnlyTask,
		contracts.ReasonixToolExplore,
		reasonixToolFleet,
		reasonixToolParallelTasks,
		reasonixToolResearch,
		reasonixToolReview,
		reasonixToolSecurityReview,
		reasonixToolRunSkill,
		reasonixToolReadOnlySkill:
		return true
	default:
		return false
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
	if !isReasonixSubagentTool(title) {
		return "", nil
	}
	return title, rawInput
}

var reasonixOutcomeHeader = regexp.MustCompile(`^(?:Subagent reference(?: \(failed\))?: sa_[\w-]*\n)?Subagent outcome: status=(completed|partial|failed|cancelled) retryable=(?:true|false)(?: error_code=\S+)?(?:\n|$)`)

// A completed background launch acknowledges dispatch, not the subagent's completion.
func reasonixSubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		return nil
	}
	status := acp.FinalStatus(tcu.Status)
	toolName, rawInput := resolveReasonixTask(tcu.Title, tcu.RawInput)
	readOnly := toolName == contracts.ReasonixToolReadOnlyTask || toolName == contracts.ReasonixToolExplore || toolName == reasonixToolReadOnlySkill
	backgroundLaunch := false
	// A successful read-only task returns plain report text, not a status envelope.
	if tcu.Status != "cancelled" && (tcu.Status != "completed" || !readOnly) {
		outcome := reasonixOutcomeHeader.FindStringSubmatch(acp.ToolCallText(tcu.Content))
		if len(outcome) > 1 {
			switch outcome[1] {
			case "failed", "partial":
				status = bgtask.StatusFailed
			case "cancelled":
				status = bgtask.StatusStopped
			}
		} else if tcu.Status == "completed" {
			var args struct {
				Background bool `json:"run_in_background"`
			}
			_ = json.Unmarshal(rawInput, &args)
			if args.Background || strings.HasPrefix(acp.ToolCallText(tcu.Content), "Started background task ") {
				backgroundLaunch = true
			}
		}
	}
	hasOutcomeEnvelope := !readOnly
	report := reasonixSubagentReport(acp.ToolCallText(tcu.Content), hasOutcomeEnvelope)
	if backgroundLaunch {
		// Reasonix sends the launch acknowledgement but no later child lifecycle over ACP.
		// Close the child row without claiming its outcome. An acknowledgement supplies no child report.
		status = bgtask.StatusEndedWithUnknownOutcome
		report = ""
	}
	return &acp.SubagentObservation{
		RowKey: tcu.ToolCallID, Status: status, CloseRow: true, Mode: acp.ModeCloseOnly,
		ReportID: tcu.ToolCallID,
		Report:   agent.SubagentReport{Text: report},
	}
}

func reasonixSubagentReport(text string, hasOutcomeEnvelope bool) string {
	if !hasOutcomeEnvelope {
		return text
	}
	text = strings.TrimSpace(text)
	text = reasonixOutcomeHeader.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	if report, ok := strings.CutPrefix(text, "Final answer:"); ok {
		return strings.TrimSpace(report)
	}
	return text
}
