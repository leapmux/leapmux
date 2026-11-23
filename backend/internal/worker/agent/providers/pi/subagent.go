package pi

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The pi-subagents extension reports each child agent that Pi spawns. The helpers
// in this file map its tool results and its notifications onto background-task
// registry rows.

// piToolGetSubagentResult is the Go-only tool name for pi-subagents result
// polling, which suppresses a separate subagent-notification when it returns a
// final status.
const piToolGetSubagentResult = "get_subagent_result"

func (a *Agent) trackOpenBgTask(rowKey string) {
	if rowKey == "" {
		return
	}
	a.Mu.Lock()
	if a.openBgTasks == nil {
		a.openBgTasks = make(map[string]struct{})
	}
	a.openBgTasks[rowKey] = struct{}{}
	a.Mu.Unlock()
}

func (a *Agent) untrackOpenBgTask(rowKey string) {
	if rowKey == "" {
		return
	}
	a.Mu.Lock()
	delete(a.openBgTasks, rowKey)
	a.Mu.Unlock()
}

func (a *Agent) renameOpenBgTask(oldKey, newKey string) {
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return
	}
	a.Mu.Lock()
	if _, ok := a.openBgTasks[oldKey]; ok {
		delete(a.openBgTasks, oldKey)
		a.openBgTasks[newKey] = struct{}{}
	}
	a.Mu.Unlock()
}

func (a *Agent) closeOpenBgTasks(status bgtask.Status) {
	a.Mu.Lock()
	rowKeys := make([]string, 0, len(a.openBgTasks))
	for rowKey := range a.openBgTasks {
		rowKeys = append(rowKeys, rowKey)
	}
	clear(a.openBgTasks)
	a.Mu.Unlock()
	if a.sink == nil {
		return
	}
	for _, rowKey := range rowKeys {
		providerkit.LogRegistryRefusal("pi", "close", a.sink.CloseBackgroundTask(rowKey, status))
	}
}

func piSubagentStatusForCompletion(completion agent.MessageCompletion) bgtask.Status {
	switch completion {
	case agent.MessageCompletionError:
		return bgtask.StatusFailed
	case agent.MessageCompletionFinished:
		return bgtask.StatusEndedWithUnknownOutcome
	default:
		return bgtask.StatusStopped
	}
}

// toolCallTitle returns the description recorded at tool_execution_start for
// the registry title (empty when none was recorded).
func (a *Agent) toolCallTitle(toolCallID string) string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if state := a.toolStates[toolCallID]; state != nil {
		return state.Description
	}
	return ""
}

// logUpsertRefusal records a background-task row the registry REFUSED.
//
// A thin name over the shared `providerkit.LogRegistryRefusal`, kept because these
// call sites read better without the two constant arguments repeated at each
// one. The RULE is the shared helper's; this only states the provider once.
func logUpsertRefusal(err error) {
	providerkit.LogRegistryRefusal("pi", "upsert", err)
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

// piExtractResumeID pulls the target subagent ID out of an Agent({resume})
// tool_execution_start input, or "" when absent.
func piExtractResumeID(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		Resume string `json:"resume"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	return strings.TrimSpace(in.Resume)
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
// status. completed/steered→Completed, error/failed→Failed, stopped/aborted→Stopped.
func piFinalStatus(s string) (bgtask.Status, bool) {
	switch s {
	case "completed", "steered":
		return bgtask.StatusSucceeded, true
	case "error", "failed":
		return bgtask.StatusFailed, true
	case "stopped", "aborted":
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusSucceeded, false
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
		childID, err = sink.EnsureChildAgent(agent.ChildAgentSpec{
			RegistryRole: bgtask.RoleChildTask,
			SpawnSpanID:  spawnSpanID, ProviderChildKey: providerChildKey, Title: title})
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
		if block.Type == ContentBlockText {
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
	piApplySubagentEndState(sink, result, toolCallID, toolCallID, title, prompt, false, false)
}

// piApplySubagentEndState applies a tool_execution_end outcome for an Agent
// call, returning the effective registry row key and whether the row remains
// running in the background.
func piApplySubagentEndState(sink subagentServices, result json.RawMessage, toolCallID, rowKey, title, prompt string, isError, resumed bool) (string, bool) {
	currentKey := rowKey
	if currentKey == "" {
		currentKey = toolCallID
	}
	var envelope piPartialResult
	var d piSubagentDetails
	if len(result) > 0 && json.Unmarshal(result, &envelope) == nil && json.Unmarshal(envelope.Details, &d) == nil && d.Status != "" {
		if !resumed {
			childID := ""
			if d.AgentID != "" {
				var err error
				childID, _, _, err = sink.LookupBackgroundTask(d.AgentID)
				if err != nil {
					slog.Warn("pi subagent stable child lookup failed", "agent_id", d.AgentID, "error", err)
				}
			}
			if childID == "" {
				piEnsureSubagentChild(sink, toolCallID, currentKey, currentKey, title, prompt)
			}
		}
		// A rename that failed leaves the row under its original key. The final status
		// must still reach that key, or the registry keeps a Running row for a call
		// that ended: piApplySubagentNotification cannot repair it, because it writes
		// to the agent id and the row is not there.
		renamed := true
		if d.AgentID != "" && d.AgentID != currentKey {
			if err := sink.RenameBackgroundTask(currentKey, d.AgentID); err != nil {
				slog.Warn("pi rename subagent task failed", "tool_call_id", toolCallID, "error", err)
				renamed = false
			}
		}
		finalKey := d.AgentID
		if finalKey == "" || !renamed {
			finalKey = currentKey
		}
		if d.Status == "background" && !isError {
			if !resumed && renamed && d.AgentID != "" && d.AgentID != currentKey {
				piEnsureSubagentChild(sink, toolCallID, d.AgentID, d.AgentID, title, prompt)
			}
			// No agent id, or a rename that failed: the row stays keyed by currentKey
			// as-is (still running).
			return finalKey, true
		}
		// An unrecognized status must NOT give a final status to the row unless
		// the tool call itself failed (isError). Otherwise keep it Running so a
		// future final event can still close it.
		status, ok := piFinalStatus(d.Status)
		if !ok && isError {
			status, ok = bgtask.StatusFailed, true
		}
		if report := piSubagentReportText(result); report != "" {
			reportID := finalKey
			if resumed {
				reportID = toolCallID
			}
			providerkit.PersistChildSubagentReport(sink, agent.ChildSubagentReportWrite{
				RowKey: finalKey,
				Write: agent.SubagentReportWrite{
					ReportID: reportID,
					Report:   agent.SubagentReport{Text: report},
				},
			})
		}
		if ok {
			if resumed {
				providerkit.LogRegistryRefusal("pi", "close", sink.CloseBackgroundTask(finalKey, status))
			} else {
				// A final-status upsert already stamps ended_at and the monotonic-final
				// guard makes the row absorbing; no separate CloseBackgroundTask needed
				// (it would early-return on the now-finished row).
				logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: finalKey, Kind: bgtask.KindSubagent, Title: title, Status: status}))
			}
			return finalKey, false
		}
		upsertTitle := title
		if resumed {
			upsertTitle = ""
		}
		logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: finalKey, Kind: bgtask.KindSubagent, Title: upsertTitle, ActiveForm: d.Activity, Status: bgtask.StatusRunning}))
		return finalKey, true
	}
	// Fallback: regex over the result text for an Agent ID (a background agent
	// whose details did not parse). Rename the row from currentKey to the
	// captured agent ID so later notifications and get_subagent_result calls
	// resolve the same row. Free-form prose that mentions "Agent ID:"
	// mid-sentence does not match the anchored regex.
	if !isError && len(result) > 0 {
		var content strings.Builder
		for _, block := range envelope.Content {
			if block.Type == ContentBlockText {
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
				agentID := m[1]
				if !resumed {
					piEnsureSubagentChild(sink, toolCallID, currentKey, currentKey, title, prompt)
				}
				finalKey := agentID
				if finalKey != currentKey {
					if err := sink.RenameBackgroundTask(currentKey, finalKey); err != nil {
						slog.Warn("pi rename subagent task failed", "tool_call_id", toolCallID, "error", err)
						finalKey = currentKey
					}
				}
				rowTitle := title
				if resumed {
					rowTitle = ""
				} else if rowTitle == "" {
					rowTitle = "background agent " + agentID
				}
				logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: finalKey, Kind: bgtask.KindSubagent, Title: rowTitle, Status: bgtask.StatusRunning,
				}))
				return finalKey, true
			}
		}
	}
	// No details.status and no background Agent ID: close any open row for this
	// call without creating a phantom row when none exists.
	status := bgtask.StatusStopped
	if isError {
		status = bgtask.StatusFailed
	}
	if currentKey != "" {
		providerkit.LogRegistryRefusal("pi", "close", sink.CloseBackgroundTask(currentKey, status))
	}
	return currentKey, false
}

// piApplyGetSubagentResult closes a background subagent row when a
// get_subagent_result tool call returns a final status (suppressing a separate
// subagent-notification). Returns the closed agent ID, or "" when no row closed.
func piApplyGetSubagentResult(sink subagentServices, args, result json.RawMessage) string {
	if sink == nil || len(result) == 0 {
		return ""
	}
	var envelope piPartialResult
	if json.Unmarshal(result, &envelope) != nil {
		return ""
	}
	var details struct {
		AgentID       string `json:"agentId"`
		ID            string `json:"id"`
		Status        string `json:"status"`
		ResultPreview string `json:"resultPreview"`
	}
	if len(envelope.Details) > 0 {
		_ = json.Unmarshal(envelope.Details, &details)
	}
	agentID := strings.TrimSpace(details.AgentID)
	if agentID == "" {
		agentID = strings.TrimSpace(details.ID)
	}
	if agentID == "" && len(args) > 0 {
		var call struct {
			AgentID string `json:"agentId"`
			ID      string `json:"id"`
		}
		if json.Unmarshal(args, &call) == nil {
			agentID = strings.TrimSpace(call.AgentID)
			if agentID == "" {
				agentID = strings.TrimSpace(call.ID)
			}
		}
	}
	if agentID == "" || details.Status == "" {
		return ""
	}
	status, ok := piFinalStatus(details.Status)
	if !ok {
		return ""
	}
	report := piSubagentReportText(result)
	if report == "" {
		report = strings.TrimSpace(details.ResultPreview)
	}
	if report != "" {
		providerkit.PersistChildSubagentReport(sink, agent.ChildSubagentReportWrite{
			RowKey: agentID,
			Write: agent.SubagentReportWrite{
				ReportID: agentID,
				Report:   agent.SubagentReport{Text: report},
			},
		})
	}
	providerkit.LogRegistryRefusal("pi", "close", sink.CloseBackgroundTask(agentID, status))
	return agentID
}

// piApplySubagentNotification sniffs a customType:"subagent-notification"
// message and updates/closes the registry from its details (including
// details.others[] for group nudges). The message itself still persists.
func piApplySubagentNotification(sink subagentServices, raw []byte) {
	piApplySubagentNotificationTracked(sink, raw, nil, nil)
}

func (a *Agent) applyPiSubagentNotification(raw []byte) {
	piApplySubagentNotificationTracked(a.sink, raw, a.trackOpenBgTask, a.untrackOpenBgTask)
}

func piApplySubagentNotificationTracked(sink subagentServices, raw []byte, onOpen, onClose func(string)) {
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
			providerkit.PersistChildSubagentReport(sink, agent.ChildSubagentReportWrite{
				RowKey: d.ID,
				Write: agent.SubagentReportWrite{
					ReportID: d.ID,
					Report:   agent.SubagentReport{Text: d.Result},
				},
			})
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: d.ID, Kind: bgtask.KindSubagent, Title: d.Description, Status: status}))
			if onClose != nil {
				onClose(d.ID)
			}
		} else {
			logUpsertRefusal(sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: d.ID, Kind: bgtask.KindSubagent, Title: d.Description, Status: bgtask.StatusRunning}))
			if onOpen != nil {
				onOpen(d.ID)
			}
		}
	}
	applyOne(envelope.Message.Details.details)
	for _, other := range envelope.Message.Details.Others {
		applyOne(other)
	}
}

// subagentServices is the part of the provider services that Pi's subagent
// tool uses: it opens the child, keeps the child's background-task row, and
// persists the child's prompt and report.
type subagentServices interface {
	agent.ChildServices
	agent.BackgroundTaskServices
	agent.SessionServices
}
