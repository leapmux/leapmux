package agent

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The pi-subagents extension reports each child agent that Pi spawns. The helpers
// in this file map its tool results and its notifications onto background-task
// registry rows.

// toolCallTitle returns the description recorded at tool_execution_start for
// the registry title (empty when none was recorded).
func (a *PiAgent) toolCallTitle(toolCallID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if state := a.toolStates[toolCallID]; state != nil {
		return state.Description
	}
	return ""
}

// logUpsertRefusal records a background-task row the registry REFUSED.
//
// A thin name over the shared `logRegistryRefusal`, kept because these seven
// call sites read better without the two constant arguments repeated at each
// one. The RULE is the shared helper's; this only states the provider once.
func logUpsertRefusal(err error) {
	logRegistryRefusal("pi", "upsert", err)
}

// piExtractDescription pulls a human label out of a tool_execution_start input.
// The pi-subagents extension carries the spawn prompt as `description` (and a
// `prompt`); fall back to the tool name.
func piExtractDescription(input json.RawMessage, toolName string) string {
	if len(input) == 0 {
		return toolName
	}
	var in struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if json.Unmarshal(input, &in) == nil {
		// Both branches take the same cap. The description arrives as a
		// label the model wrote, so it is no more capped than the prompt is,
		// and a caller that reads one branch must not have to know which.
		//
		// CLEAN FIRST, THEN TEST. A field that holds only characters a reader
		// cannot see -- a run of zero-width spaces, a lone bidirectional mark --
		// is non-empty as bytes and empty as text, so testing the RAW field
		// entered the branch and then returned "": the row lost the prompt
		// fallback AND the tool-name fallback, and a Pi subagent appeared in the
		// sidebar with no label at all. `acpBridge.terminal/create` orders these
		// the same way.
		if desc := bgtask.CleanTitleRunes(bgtask.FirstLine(in.Description), 80); desc != "" {
			return desc
		}
		if prompt := bgtask.CleanTitleRunes(bgtask.FirstLine(in.Prompt), 80); prompt != "" {
			return prompt
		}
	}
	return toolName
}

// piExtractPrompt pulls the whole spawn prompt out of a tool_execution_start
// input, or "" when the tool carries none. Distinct from
// piExtractDescription, which wants a short label and truncates to one line.
func piExtractPrompt(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	return in.Prompt
}

// piSubagentDetails contains child status from Agent result details.
type piSubagentDetails struct {
	Status   string `json:"status"`
	Activity string `json:"activity"`
	AgentID  string `json:"agentId"`
}

// piSubagentFromDetails upserts a running registry row when details parse to
// the subagent shape. Returns nil for a non-subagent details blob.
func piSubagentFromDetails(details json.RawMessage, toolCallID, title string) *bgtask.Upsert {
	if len(details) == 0 {
		return nil
	}
	var d piSubagentDetails
	if json.Unmarshal(details, &d) != nil || d.Status == "" {
		return nil
	}
	rowKey := d.AgentID
	if rowKey == "" {
		rowKey = toolCallID
	}
	return &bgtask.Upsert{
		RowKey:     rowKey,
		Kind:       bgtask.KindSubagent,
		Title:      title,
		ActiveForm: d.Activity,
		Status:     bgtask.StatusRunning,
	}
}

// piFinalStatus maps a pi-subagents result status to the registry final
// status. completed/steered→Completed, error→Failed, stopped/aborted→Stopped.
func piFinalStatus(s string) (bgtask.Status, bool) {
	switch s {
	case "completed", "steered":
		return bgtask.StatusCompleted, true
	case "error":
		return bgtask.StatusFailed, true
	case "stopped", "aborted":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusCompleted, false
	}
}

func piEnsureSubagentChild(sink subagentServices, spawnSpanID, rowKey, providerChildKey, title, prompt string) string {
	if sink == nil || spawnSpanID == "" || rowKey == "" || providerChildKey == "" {
		return ""
	}
	childID, _, found, err := sink.LookupBackgroundTask(rowKey)
	if err != nil {
		slog.Warn("pi subagent child lookup failed", "row_key", rowKey, "error", err)
		return ""
	}
	if !found || childID == "" {
		childID, err = sink.EnsureChildAgent(spawnSpanID, providerChildKey, title)
		if err != nil {
			slog.Warn("pi ensure subagent child failed", "row_key", rowKey, "error", err)
			return ""
		}
	}
	if err := sink.PersistChildPrompt(childID, prompt); err != nil {
		slog.Warn("pi persist subagent prompt failed", "row_key", rowKey, "error", err)
	}
	logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: rowKey, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: title, Status: bgtask.StatusRunning,
	}))
	return childID
}

func piSubagentReportText(result json.RawMessage) string {
	var envelope piPartialResult
	if json.Unmarshal(result, &envelope) != nil {
		return ""
	}
	blocks := make([]string, 0, len(envelope.Content))
	for _, block := range envelope.Content {
		if block.Type == PiContentBlockText {
			blocks = append(blocks, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(blocks, "\n"))
}

// piAgentIDRe matches a standalone "Agent ID: <id>" line in a Pi tool result.
// Anchored to a line start so free-form model prose that merely mentions
// "Agent ID:" mid-sentence does not produce a phantom registry row.
var piAgentIDRe = regexp.MustCompile(`(?m)^Agent ID: (\S+)\s*$`)

// piApplySubagentEnd parses a tool_execution_end result for final status or
// a background re-key. status:"background" re-keys the row to details.agentId
// and leaves it Running (fallback: regex "Agent ID: (\S+)" over result text).
func piApplySubagentEnd(sink subagentServices, result json.RawMessage, toolCallID, title, prompt string) {
	if len(result) == 0 {
		return
	}
	var envelope piPartialResult
	var d piSubagentDetails
	if json.Unmarshal(result, &envelope) == nil && json.Unmarshal(envelope.Details, &d) == nil && d.Status != "" {
		childID := ""
		if d.AgentID != "" {
			var err error
			childID, _, _, err = sink.LookupBackgroundTask(d.AgentID)
			if err != nil {
				slog.Warn("pi subagent stable child lookup failed", "agent_id", d.AgentID, "error", err)
			}
		}
		if childID == "" {
			piEnsureSubagentChild(sink, toolCallID, toolCallID, toolCallID, title, prompt)
		}
		// A rename that failed leaves the row under its original key. The final status
		// must still reach that key, or the registry keeps a Running row for a call
		// that ended: piApplySubagentNotification cannot repair it, because it writes
		// to the agent id and the row is not there.
		renamed := true
		if d.AgentID != "" && d.AgentID != toolCallID {
			if err := sink.RenameBackgroundTask(toolCallID, d.AgentID); err != nil {
				slog.Warn("pi rename subagent task failed", "tool_call_id", toolCallID, "error", err)
				renamed = false
			}
		}
		if d.Status == "background" {
			if agentID := d.AgentID; renamed && agentID != "" && agentID != toolCallID {
				piEnsureSubagentChild(sink, toolCallID, agentID, agentID, title, prompt)
			}
			// No agent id, or a rename that failed: the row stays keyed by toolCallID
			// as-is (still running).
			return
		}
		// An unrecognized status must NOT give a final status to the row (piFinalStatus
		// returns ok=false for it). Upsert as Running so a future final event
		// can still close it, matching piApplySubagentNotification's contract.
		status, ok := piFinalStatus(d.Status)
		rowKey := d.AgentID
		if rowKey == "" || !renamed {
			rowKey = toolCallID
		}
		if report := piSubagentReportText(result); report != "" {
			reportID := d.AgentID
			if reportID == "" || !renamed {
				reportID = toolCallID
			}
			persistSubagentReport(sink, SubagentReportWrite{
				ReportID: reportID,
				RowKey:   rowKey,
				Target:   SubagentReportChildTranscript,
				Report:   SubagentReport{Text: report},
			})
		}
		if ok {
			// A final-status upsert already stamps ended_at and the monotonic-final
			// guard makes the row absorbing; no separate CloseBackgroundTask needed
			// (it would early-return on the now-finished row).
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, Title: title, Status: status}))
		} else {
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, Title: title, ActiveForm: d.Activity, Status: bgtask.StatusRunning}))
		}
		return
	}
	// Fallback: regex over the result text for an Agent ID (a background agent
	// whose details did not parse). Key the row off the deterministic toolCallID
	// so a later final event can close it; the captured agent id only refines
	// the title. Free-form prose that mentions "Agent ID:" mid-sentence does not
	// match the anchored regex. The result may be a JSON-encoded string, so
	// decode it first; fall back to the raw text if it is not a string.
	var content strings.Builder
	for _, block := range envelope.Content {
		if block.Type == PiContentBlockText {
			content.WriteString(block.Text)
		}
	}
	s := content.String()
	var asString string
	if json.Unmarshal(result, &asString) == nil {
		s = asString
	}
	if strings.Contains(s, "Agent ID:") {
		if m := piAgentIDRe.FindStringSubmatch(s); len(m) > 1 {
			piEnsureSubagentChild(sink, toolCallID, toolCallID, toolCallID, title, prompt)
			rowTitle := title
			if rowTitle == "" {
				rowTitle = "background agent " + m[1]
			}
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{
				RowKey: toolCallID, Kind: bgtask.KindSubagent, Title: rowTitle, Status: bgtask.StatusRunning,
			}))
		}
	}
}

// piApplySubagentNotification sniffs a customType:"subagent-notification"
// message and updates/closes the registry from its details (including
// details.others[] for group nudges). The message itself still persists.
func piApplySubagentNotification(sink subagentServices, raw []byte) {
	type details struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Description string `json:"description"`
		Result      string `json:"resultPreview"`
	}
	var envelope struct {
		Message struct {
			Role       string `json:"role"`
			CustomType string `json:"customType"`
			Details    struct {
				details
				Others []details `json:"others"`
			} `json:"details"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Message.Role != "custom" || envelope.Message.CustomType != contracts.PiCustomTypeSubagentNotification {
		return
	}
	applyOne := func(d details) {
		if d.ID == "" || d.Status == "" {
			return
		}
		if status, ok := piFinalStatus(d.Status); ok {
			persistSubagentReport(sink, SubagentReportWrite{
				ReportID: d.ID,
				RowKey:   d.ID,
				Target:   SubagentReportChildTranscript,
				Report:   SubagentReport{Text: d.Result},
			})
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: d.ID, Kind: bgtask.KindSubagent, Title: d.Description, Status: status}))
		} else {
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: d.ID, Kind: bgtask.KindSubagent, Title: d.Description, Status: bgtask.StatusRunning}))
		}
	}
	applyOne(envelope.Message.Details.details)
	for _, other := range envelope.Message.Details.Others {
		applyOne(other)
	}
}
